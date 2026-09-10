package providers

import (
	"context"
	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"io"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"net/http"
	"strings"
	"testing"
)

type awsInventoryTransport func(*http.Request) (*http.Response, error)

func (f awsInventoryTransport) Do(r *http.Request) (*http.Response, error) { return f(r) }
func TestAWSDirectInventoryNativePagination(t *testing.T) {
	cases := []struct{ op, action, body, id string }{
		{"ec2.describe_instances", "DescribeInstances", `<reservationSet><item><instancesSet><item><instanceId>i-123</instanceId></item></instancesSet></item></reservationSet><nextToken>next</nextToken>`, "i-123"},
		{"ec2.describe_volumes", "DescribeVolumes", `<volumeSet><item><volumeId>vol-123</volumeId><size>42</size></item></volumeSet><nextToken>next</nextToken>`, "vol-123"},
		{"ec2.describe_vpcs", "DescribeVpcs", `<vpcSet><item><vpcId>vpc-123</vpcId></item></vpcSet><nextToken>next</nextToken>`, "vpc-123"},
		{"ec2.describe_subnets", "DescribeSubnets", `<subnetSet><item><subnetId>subnet-123</subnetId></item></subnetSet><nextToken>next</nextToken>`, "subnet-123"},
		{"ec2.describe_security_groups", "DescribeSecurityGroups", `<securityGroupInfo><item><groupId>sg-123</groupId></item></securityGroupInfo><nextToken>next</nextToken>`, "sg-123"},
		{"ec2.describe_addresses", "DescribeAddresses", `<addressesSet><item><allocationId>eipalloc-123</allocationId></item></addressesSet>`, "eipalloc-123"},
		{"ec2.describe_nat_gateways", "DescribeNatGateways", `<natGatewaySet><item><natGatewayId>nat-123</natGatewayId></item></natGatewaySet><nextToken>next</nextToken>`, "nat-123"},
		{"rds.describe_db_instances", "DescribeDBInstances", `<DescribeDBInstancesResult><DBInstances><DBInstance><DBInstanceIdentifier>db-123</DBInstanceIdentifier></DBInstance></DBInstances><Marker>next</Marker></DescribeDBInstancesResult>`, "db-123"},
		{"eks.list_clusters", "", `{"clusters":["cluster-123"],"nextToken":"next"}`, "cluster-123"},
		{"elb.describe_load_balancers", "DescribeLoadBalancers", `<DescribeLoadBalancersResult><LoadBalancerDescriptions><member><LoadBalancerName>classic</LoadBalancerName></member></LoadBalancerDescriptions><NextMarker>next</NextMarker></DescribeLoadBalancersResult>`, "classic"},
		{"elbv2.describe_load_balancers", "DescribeLoadBalancers", `<DescribeLoadBalancersResult><LoadBalancers><member><LoadBalancerArn>arn:aws-cn:elasticloadbalancing:cn-northwest-1:123456789012:loadbalancer/app/test/id</LoadBalancerArn></member></LoadBalancers><NextMarker>next</NextMarker></DescribeLoadBalancersResult>`, "arn:aws-cn:elasticloadbalancing:cn-northwest-1:123456789012:loadbalancer/app/test/id"},
		{"s3.list_buckets", "", `<ListAllMyBucketsResult><Buckets><Bucket><Name>bucket-123</Name><BucketRegion>cn-northwest-1</BucketRegion></Bucket></Buckets><ContinuationToken>next</ContinuationToken></ListAllMyBucketsResult>`, "bucket-123"},
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			calls := 0
			cfg := awsbase.Config{Region: "cn-northwest-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: awsInventoryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if !strings.Contains(r.URL.Host, "cn-northwest-1") || !strings.HasSuffix(r.URL.Host, "amazonaws.com.cn") {
					t.Fatalf("wrong partition endpoint: %s", r.URL.Host)
				}
				var body []byte
				if r.Body != nil {
					body, _ = io.ReadAll(r.Body)
				}
				request := string(body) + r.URL.RawQuery
				if tc.action != "" && !strings.Contains(request, "Action="+tc.action) {
					t.Fatalf("wrong action: %s", request)
				}
				if tc.op != "ec2.describe_addresses" && !strings.Contains(request, "previous") {
					t.Fatal("lost native input token")
				}
				if tc.op == "s3.list_buckets" && r.URL.Query().Get("bucket-region") != "cn-northwest-1" {
					t.Fatal("S3 regional filter missing")
				}
				response := tc.body
				contentType := "application/json"
				if tc.op == "s3.list_buckets" {
					contentType = "text/xml"
				}
				if tc.action != "" {
					response = "<" + tc.action + "Response>" + tc.body + "</" + tc.action + "Response>"
					contentType = "text/xml"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
			})}
			token := "previous"
			if tc.op == "ec2.describe_addresses" {
				token = ""
			}
			a := &awsAdapter{name: "test"}
			page, err := a.fetchDirectInventory(context.Background(), cfg, provider.NativeRequest{Operation: "aws." + tc.op, PageToken: token}, cfg.Region, "123456789012", 20)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(page.Rows) != 1 || page.Rows[0]["id"] != tc.id {
				t.Fatalf("unexpected page: %+v", page)
			}
			wantNext := "next"
			if tc.op == "ec2.describe_addresses" {
				wantNext = ""
			}
			if page.NextToken != wantNext {
				t.Fatalf("lost output token: %+v", page)
			}
		})
	}
}
func TestAWSDirectInventoryRejectsInvalidRequests(t *testing.T) {
	a := &awsAdapter{profile: config.Profile{Regions: []string{"cn-northwest-1"}}}
	a.profile.Scopes.Accounts = []string{"123456789012"}
	for _, op := range awsDirectOperations() {
		for _, req := range []provider.NativeRequest{
			{Operation: op.Name, Region: "us-east-1"},
			{Operation: op.Name, Region: "cn-northwest-1", Params: map[string]any{"endpoint": "https://example.com"}},
		} {
			if _, err := a.NativeRead(context.Background(), req); err == nil {
				t.Fatalf("accepted invalid request: %+v", req)
			}
		}
	}
	for _, req := range []provider.NativeRequest{
		{Operation: "aws.ec2.describe_instances", Limit: 1},
		{Operation: "aws.rds.describe_db_instances", Limit: 19},
		{Operation: "aws.eks.list_clusters", Limit: 101},
		{Operation: "aws.ec2.describe_addresses", PageToken: "invalid"},
	} {
		req.Region = "cn-northwest-1"
		if _, err := a.NativeRead(context.Background(), req); err == nil {
			t.Fatalf("accepted invalid pagination: %+v", req)
		}
	}
}
