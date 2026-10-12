package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"mcpcloud/internal/provider"
)

func TestAWSRawServicePaginationAndSafeRows(t *testing.T) {
	cases := []struct {
		operation string
		params    map[string]any
		body      string
		wantID    string
		wantType  string
		wantNext  string
	}{
		{
			operation: "aws.elasticache.describe_cache_clusters",
			body:      `<DescribeCacheClustersResponse><DescribeCacheClustersResult><CacheClusters><CacheCluster><CacheClusterId>cache-a</CacheClusterId><Engine>redis</Engine><CacheClusterStatus>available</CacheClusterStatus><NumCacheNodes>2</NumCacheNodes></CacheCluster></CacheClusters><Marker>cache-next</Marker></DescribeCacheClustersResult></DescribeCacheClustersResponse>`,
			wantID:    "cache-a", wantType: "AWS::ElastiCache::CacheCluster", wantNext: "cache-next",
		},
		{
			operation: "aws.elasticache.describe_replication_groups",
			body:      `<DescribeReplicationGroupsResponse><DescribeReplicationGroupsResult><ReplicationGroups><ReplicationGroup><ReplicationGroupId>group-a</ReplicationGroupId><Engine>redis</Engine><Status>available</Status></ReplicationGroup></ReplicationGroups><Marker>group-next</Marker></DescribeReplicationGroupsResult></DescribeReplicationGroupsResponse>`,
			wantID:    "group-a", wantType: "AWS::ElastiCache::ReplicationGroup", wantNext: "group-next",
		},
		{
			operation: "aws.ecs.list_services",
			params:    map[string]any{"cluster_arn": "arn:aws-cn:ecs:cn-northwest-1:123456789012:cluster/cluster-a"},
			body:      `{"serviceArns":["arn:aws-cn:ecs:cn-northwest-1:123456789012:service/cluster-a/service-a"],"nextToken":"ecs-next"}`,
			wantID:    "arn:aws-cn:ecs:cn-northwest-1:123456789012:service/cluster-a/service-a", wantType: "AWS::ECS::Service", wantNext: "ecs-next",
		},
	}
	for _, tc := range cases {
		t.Run(tc.operation, func(t *testing.T) {
			pageToken := "previous"
			if strings.HasPrefix(tc.operation, "aws.elasticache.") {
				pageToken = encodeRawCacheCursor(rawCacheCursor{Marker: "previous"})
			}
			cfg := awsbase.Config{
				Region:      "cn-northwest-1",
				Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
				HTTPClient: awsInventoryTransport(func(r *http.Request) (*http.Response, error) {
					if !strings.HasSuffix(r.URL.Host, "amazonaws.com.cn") {
						t.Fatalf("wrong partition endpoint: %s", r.URL.Host)
					}
					if r.URL.Query().Get("Marker") != "previous" && r.Header.Get("X-Amz-Target") == "" {
						t.Fatalf("lost ElastiCache cursor: %s", r.URL.RawQuery)
					}
					if strings.HasPrefix(tc.operation, "aws.elasticache.") && r.URL.Query().Get("MaxRecords") != "20" {
						t.Fatalf("ElastiCache MaxRecords = %s, want 20", r.URL.Query().Get("MaxRecords"))
					}
					if strings.HasPrefix(tc.operation, "aws.ecs.") {
						payload, _ := io.ReadAll(r.Body)
						if !strings.Contains(string(payload), `"nextToken":"previous"`) || !strings.Contains(string(payload), `"cluster"`) {
							t.Fatalf("invalid ECS request: %s", payload)
						}
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/xml"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
				}),
			}
			a := &awsAdapter{name: "test"}
			page, err := a.fetchDirectInventory(context.Background(), cfg, provider.NativeRequest{Operation: tc.operation, Params: tc.params, PageToken: pageToken}, cfg.Region, "123456789012", 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rows) != 1 {
				t.Fatalf("unexpected row count: %#v", page)
			}
			resourceType := ""
			if native, ok := page.Rows[0]["native"].(map[string]any); ok {
				resourceType, _ = native["resource_type"].(string)
			}
			gotNext := page.NextToken
			if strings.HasPrefix(tc.operation, "aws.elasticache.") {
				cursor, err := decodeRawCacheCursor(page.NextToken, tc.operation)
				if err != nil {
					t.Fatal(err)
				}
				gotNext = cursor.Marker
			}
			if page.Rows[0]["id"] != tc.wantID || resourceType != tc.wantType || gotNext != tc.wantNext {
				t.Fatalf("unexpected page: %#v", page)
			}
			if tc.operation == "aws.ecs.list_services" && page.Rows[0]["domain"] != "compute" {
				t.Fatalf("ECS service domain = %v, want compute", page.Rows[0]["domain"])
			}
		})
	}
}

func TestAWSRawElastiCacheHonorsSmallMCPPages(t *testing.T) {
	body := `<DescribeCacheClustersResponse><DescribeCacheClustersResult><CacheClusters>` +
		`<CacheCluster><CacheClusterId>a</CacheClusterId></CacheCluster>` +
		`<CacheCluster><CacheClusterId>b</CacheClusterId></CacheCluster>` +
		`<CacheCluster><CacheClusterId>c</CacheClusterId></CacheCluster>` +
		`<CacheCluster><CacheClusterId>d</CacheClusterId></CacheCluster>` +
		`<CacheCluster><CacheClusterId>e</CacheClusterId></CacheCluster>` +
		`<CacheCluster><CacheClusterId>f</CacheClusterId></CacheCluster>` +
		`</CacheClusters><Marker>next-provider-page</Marker></DescribeCacheClustersResult></DescribeCacheClustersResponse>`
	calls := 0
	cfg := awsbase.Config{
		Region:      "cn-northwest-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
		HTTPClient: awsInventoryTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Query().Get("MaxRecords") != "20" {
				t.Fatalf("MaxRecords = %s, want 20", r.URL.Query().Get("MaxRecords"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		}),
	}
	a := &awsAdapter{name: "test"}
	request := provider.NativeRequest{Operation: "aws.elasticache.describe_cache_clusters", Limit: 5}
	first, err := a.fetchDirectInventory(context.Background(), cfg, request, cfg.Region, "123456789012", 5)
	if err != nil || len(first.Rows) != 5 || first.NextToken == "" {
		t.Fatalf("first page = %#v, err = %v", first, err)
	}
	request.PageToken = first.NextToken
	second, err := a.fetchDirectInventory(context.Background(), cfg, request, cfg.Region, "123456789012", 5)
	if err != nil || len(second.Rows) != 1 || second.Rows[0]["id"] != "f" {
		t.Fatalf("second page = %#v, err = %v", second, err)
	}
	next, err := decodeRawCacheCursor(second.NextToken, request.Operation)
	if err != nil || next.Marker != "next-provider-page" || next.Offset != 0 || calls != 2 {
		t.Fatalf("next cursor = %#v, err = %v, calls = %d", next, err, calls)
	}
}
