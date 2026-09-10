package providers

import (
	"context"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"testing"
)

type ecsIdentityStub struct {
	response *ecs.DescribeInstanceAttributeResponse
	calls    int
	cancel   context.CancelFunc
}

func (s *ecsIdentityStub) DescribeInstanceAttribute(*ecs.DescribeInstanceAttributeRequest) (*ecs.DescribeInstanceAttributeResponse, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return s.response, nil
}
func TestAlibabaECSDetailResponseIdentity(t *testing.T) {
	old := newAlibabaECSDetailClient
	t.Cleanup(func() { newAlibabaECSDetailClient = old })
	s := &ecsIdentityStub{}
	newAlibabaECSDetailClient = func(config.Profile, string) (alibabaECSDetailAPI, error) { return s, nil }
	a := &alibabaAdapter{name: "test", profile: config.Profile{Regions: []string{"cn-hangzhou"}, Scopes: config.Scopes{Accounts: []string{"123"}}}}
	r := provider.NativeRequest{Operation: "alibaba.ecs.describe_instance_attribute", Region: "cn-hangzhou", Params: map[string]any{"instance_id": "i-test"}}
	for _, tc := range []struct {
		id, region string
		want       bool
	}{{"i-test", "cn-hangzhou", true}, {"other", "cn-hangzhou", false}, {"i-test", "cn-shanghai", false}, {"", "cn-hangzhou", false}, {"i-test", "", false}} {
		s.response = ecs.CreateDescribeInstanceAttributeResponse()
		s.response.InstanceId = tc.id
		s.response.RegionId = tc.region
		p, e := a.NativeRead(context.Background(), r)
		if tc.want {
			if e != nil || len(p.Rows) != 1 || p.Rows[0]["id"] != "i-test" || p.Rows[0]["region"] != "cn-hangzhou" {
				t.Fatalf("valid response rejected: %v", e)
			}
		} else if e == nil || len(p.Rows) != 0 {
			t.Fatal("mismatched response accepted")
		}
	}
	s.response = nil
	if _, e := a.NativeRead(context.Background(), r); e == nil {
		t.Fatal("nil response accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.calls = 0
	if _, e := a.NativeRead(ctx, r); e != context.Canceled || s.calls != 0 {
		t.Fatal("cancelled request reached SDK")
	}
	ctx, cancel = context.WithCancel(context.Background())
	s.cancel = cancel
	if _, e := a.NativeRead(ctx, r); e != context.Canceled {
		t.Fatal("cancellation during SDK ignored")
	}
}
