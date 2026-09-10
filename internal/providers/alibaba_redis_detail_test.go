package providers

import (
	"context"
	"encoding/json"
	kv "github.com/aliyun/alibaba-cloud-sdk-go/services/r_kvstore"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

type redisDetailStub struct {
	response *kv.DescribeInstanceAttributeResponse
	request  *kv.DescribeInstanceAttributeRequest
}

func (s *redisDetailStub) DescribeInstanceAttribute(r *kv.DescribeInstanceAttributeRequest) (*kv.DescribeInstanceAttributeResponse, error) {
	s.request = r
	return s.response, nil
}
func TestAlibabaRedisDetailScopedAndSafe(t *testing.T) {
	old := newAlibabaRedisDetailClient
	t.Cleanup(func() { newAlibabaRedisDetailClient = old })
	stub := &redisDetailStub{response: kv.CreateDescribeInstanceAttributeResponse()}
	stub.response.Instances.DBInstanceAttribute = []kv.DBInstanceAttribute{{InstanceId: "r-test", RegionId: "cn-hangzhou", InstanceStatus: "Normal", EngineVersion: "7.0", Capacity: 1024, ConnectionDomain: "forbidden-endpoint", PrivateIp: "forbidden-ip", Port: 6379, SecurityIPList: "forbidden-allowlist", Config: "forbidden-config"}}
	newAlibabaRedisDetailClient = func(_ config.Profile, region string) (alibabaRedisDetailAPI, error) {
		if region != "cn-hangzhou" {
			t.Fatal(region)
		}
		return stub, nil
	}
	a := &alibabaAdapter{name: "test", profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123"}}, Regions: []string{"cn-hangzhou"}}}
	req := provider.NativeRequest{Operation: "alibaba.redis.describe_instance_attribute", Region: "cn-hangzhou", Params: map[string]any{"instance_id": "r-test"}}
	p, e := a.NativeRead(context.Background(), req)
	if e != nil {
		t.Fatal(e)
	}
	if stub.request.InstanceId != "r-test" || stub.request.OwnerId != "" || len(p.Rows) != 1 || p.Rows[0]["kind"] != "cache" {
		t.Fatalf("unexpected response %v", p)
	}
	b, _ := json.Marshal(p.Rows)
	for _, v := range []string{"forbidden-", "6379"} {
		if strings.Contains(string(b), v) {
			t.Fatalf("sensitive data leaked: %s", v)
		}
	}
	attrs := p.Rows[0]["attributes"].(map[string]any)
	if attrs["memory_mb"] != int64(1024) {
		t.Fatal(attrs)
	}
	for _, bad := range []provider.NativeRequest{{Operation: req.Operation, Region: "cn-shanghai", Params: req.Params}, {Operation: req.Operation, Region: req.Region, Params: map[string]any{"instance_id": "r-test", "endpoint": "https://example.com"}}, {Operation: req.Operation, Region: req.Region, Params: req.Params, PageToken: "cursor"}} {
		stub.request = nil
		if _, e := a.NativeRead(context.Background(), bad); e == nil || stub.request != nil {
			t.Fatal("invalid request reached SDK")
		}
	}
	for _, change := range []func(){func() { stub.response.Instances.DBInstanceAttribute[0].InstanceId = "other" }, func() {
		stub.response.Instances.DBInstanceAttribute[0].InstanceId = "r-test"
		stub.response.Instances.DBInstanceAttribute[0].RegionId = "cn-shanghai"
	}} {
		change()
		if _, e := a.NativeRead(context.Background(), req); e == nil {
			t.Fatal("mismatched response accepted")
		}
	}
}
