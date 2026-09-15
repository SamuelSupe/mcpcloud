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
			page, err := a.fetchDirectInventory(context.Background(), cfg, provider.NativeRequest{Operation: tc.operation, Params: tc.params, PageToken: "previous"}, cfg.Region, "123456789012", 20)
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
			if page.Rows[0]["id"] != tc.wantID || resourceType != tc.wantType || page.NextToken != tc.wantNext {
				t.Fatalf("unexpected page: %#v", page)
			}
			if tc.operation == "aws.ecs.list_services" && page.Rows[0]["domain"] != "compute" {
				t.Fatalf("ECS service domain = %v, want compute", page.Rows[0]["domain"])
			}
		})
	}
}
