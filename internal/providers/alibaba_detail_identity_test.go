package providers

import (
	"context"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cs"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/rds"
	"io"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"net/http"
	"strings"
	"testing"
)

type rdsIdentityStub struct {
	response *rds.DescribeDBInstanceAttributeResponse
	calls    int
	cancel   context.CancelFunc
}

func (s *rdsIdentityStub) DescribeDBInstanceAttribute(*rds.DescribeDBInstanceAttributeRequest) (*rds.DescribeDBInstanceAttributeResponse, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	return s.response, nil
}

type ackIdentityStub struct {
	body        string
	nilResponse bool
	calls       int
	cancel      context.CancelFunc
}

func (s *ackIdentityStub) DescribeClusterDetail(*cs.DescribeClusterDetailRequest) (*cs.DescribeClusterDetailResponse, error) {
	s.calls++
	if s.cancel != nil {
		s.cancel()
	}
	if s.nilResponse {
		return nil, nil
	}
	p := cs.CreateDescribeClusterDetailResponse()
	raw := responses.NewCommonResponse()
	_ = responses.Unmarshal(raw, &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(s.body))}, "JSON")
	p.BaseResponse = raw.BaseResponse
	return p, nil
}
func TestAlibabaDeepDetailResponseIdentity(t *testing.T) {
	oldR, oldC := newAlibabaRDSDetailClient, newAlibabaCSDetailClient
	t.Cleanup(func() { newAlibabaRDSDetailClient = oldR; newAlibabaCSDetailClient = oldC })
	rs := &rdsIdentityStub{}
	csStub := &ackIdentityStub{}
	newAlibabaRDSDetailClient = func(config.Profile, string) (alibabaRDSDetailAPI, error) { return rs, nil }
	newAlibabaCSDetailClient = func(config.Profile, string) (alibabaCSDetailAPI, error) { return csStub, nil }
	a := &alibabaAdapter{name: "test", profile: config.Profile{Regions: []string{"cn-hangzhou"}, Scopes: config.Scopes{Accounts: []string{"123"}}}}
	rr := provider.NativeRequest{Operation: "alibaba.rds.describe_db_instance_attribute", Region: "cn-hangzhou", Params: map[string]any{"db_instance_id": "rm-test"}}
	cr := provider.NativeRequest{Operation: "alibaba.cs.describe_cluster_detail", Region: "cn-hangzhou", Params: map[string]any{"cluster_id": "c-test"}}
	for _, tc := range []struct {
		id, region string
		want       bool
	}{{"rm-test", "cn-hangzhou", true}, {"other", "cn-hangzhou", false}, {"rm-test", "cn-shanghai", false}, {"rm-test", "", false}} {
		rs.response = rds.CreateDescribeDBInstanceAttributeResponse()
		rs.response.Items.DBInstanceAttribute = []rds.DBInstanceAttribute{{DBInstanceId: tc.id, RegionId: tc.region}}
		p, e := a.NativeRead(context.Background(), rr)
		if tc.want {
			if e != nil || len(p.Rows) != 1 {
				t.Fatalf("valid RDS rejected: %v", e)
			}
		} else if e == nil || len(p.Rows) != 0 {
			t.Fatal("mismatched RDS accepted")
		}
	}
	rs.response = nil
	if _, e := a.NativeRead(context.Background(), rr); e == nil {
		t.Fatal("nil RDS accepted")
	}
	for _, tc := range []struct {
		body string
		want bool
	}{{`{"cluster_id":"c-test","region_id":"cn-hangzhou"}`, true}, {`{"cluster_id":"other","region_id":"cn-hangzhou"}`, false}, {`{"cluster_id":"c-test","region_id":"cn-shanghai"}`, false}, {`{"cluster_id":"c-test"}`, false}, {`{"cluster_id":{"secret":"forbidden-value"}}`, false}} {
		csStub.body = tc.body
		p, e := a.NativeRead(context.Background(), cr)
		if tc.want {
			if e != nil || len(p.Rows) != 1 {
				t.Fatalf("valid ACK rejected: %v", e)
			}
		} else if e == nil || len(p.Rows) != 0 {
			t.Fatal("mismatched ACK accepted")
		}
		if e != nil && strings.Contains(e.Error(), "forbidden-value") {
			t.Fatal("provider payload leaked")
		}
	}
	csStub.nilResponse = true
	if _, e := a.NativeRead(context.Background(), cr); e == nil {
		t.Fatal("nil ACK accepted")
	}
	csStub.nilResponse = false
	for _, r := range []provider.NativeRequest{rr, cr} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rs.calls = 0
		csStub.calls = 0
		if _, e := a.NativeRead(ctx, r); e != context.Canceled || rs.calls+csStub.calls != 0 {
			t.Fatal("cancelled request reached SDK")
		}
		ctx, cancel = context.WithCancel(context.Background())
		rs.cancel = cancel
		csStub.cancel = cancel
		if _, e := a.NativeRead(ctx, r); e != context.Canceled {
			t.Fatal("cancellation during SDK ignored")
		}
		rs.cancel = nil
		csStub.cancel = nil
	}
}
