package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cs"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

type ackMonitoringStub struct {
	cluster, addon string
	calls          int
	request        *requests.CommonRequest
}

func (s *ackMonitoringStub) DescribeClusterDetail(r *cs.DescribeClusterDetailRequest) (*cs.DescribeClusterDetailResponse, error) {
	s.calls++
	p := cs.CreateDescribeClusterDetailResponse()
	err := responses.Unmarshal(p, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(s.cluster)), Header: http.Header{}}, "JSON")
	return p, err
}
func (s *ackMonitoringStub) ProcessCommonRequest(r *requests.CommonRequest) (*responses.CommonResponse, error) {
	s.calls++
	s.request = r
	p := responses.NewCommonResponse()
	err := responses.Unmarshal(p, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(s.addon)), Header: http.Header{}}, "JSON")
	return p, err
}

func TestAlibabaACKMonitoringScopeAndConfig(t *testing.T) {
	old := newAlibabaACKMonitoringClient
	t.Cleanup(func() { newAlibabaACKMonitoringClient = old })
	s := &ackMonitoringStub{cluster: `{"cluster_id":"c-test","region_id":"cn-hangzhou","name":"test"}`}
	newAlibabaACKMonitoringClient = func(_ config.Profile, region string) (alibabaACKMonitoringAPI, error) {
		if region != "cn-hangzhou" {
			t.Fatal(region)
		}
		return s, nil
	}
	a := &alibabaAdapter{name: "test", profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123"}}, Regions: []string{"cn-hangzhou"}}}
	r := provider.NativeRequest{Operation: alibabaACKMonitoringOperation, Region: "cn-hangzhou", Params: map[string]any{"cluster_id": "c-test"}}
	for _, tc := range []struct{ config, want string }{
		{`{"CmsEnabled":true,"secret":"forbidden-secret"}`, "enabled"},
		{`"{\"CmsEnabled\":false,\"token\":\"forbidden-token\"}"`, "disabled"},
		{`{"CmsEnabled":"true"}`, "enabled"},
		{`{}`, "unknown"}, {`null`, "unknown"}, {`{"CmsEnabled":null}`, "unknown"}, {`{"CmsEnabled":123}`, "unknown"},
	} {
		s.calls = 0
		s.addon = `{"name":"metrics-server","state":"active","version":"v1","config":` + tc.config + `,"endpoint":"forbidden-endpoint"}`
		p, err := a.NativeRead(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if p.Requests != 2 || s.calls != 2 || len(p.Rows) != 1 {
			t.Fatalf("unexpected page: %+v", p)
		}
		m := p.Rows[0]["attributes"].(map[string]any)["metrics_server"].(map[string]any)
		if m["cloudmonitor_collection"] != tc.want {
			t.Fatalf("got %v want %s", m, tc.want)
		}
		b, _ := json.Marshal(p.Rows)
		if strings.Contains(string(b), "forbidden-") {
			t.Fatal("raw configuration leaked")
		}
		if s.request.Method != "GET" || s.request.PathPattern != "/clusters/c-test/addon_instances/metrics-server" || s.request.ApiName != "GetClusterAddonInstance" || s.request.Domain != "" {
			t.Fatalf("unbounded request: %+v", s.request)
		}
	}
	for _, bad := range []provider.NativeRequest{
		{Operation: r.Operation, Region: "cn-shanghai", Params: r.Params},
		{Operation: r.Operation, Region: r.Region, Params: r.Params, PageToken: "bad"},
		{Operation: r.Operation, Region: r.Region, Params: map[string]any{"cluster_id": "c-test", "endpoint": "https://example.com"}},
		{Operation: r.Operation, Region: r.Region, Params: map[string]any{"cluster_id": "c-test", "instance_name": "other"}},
	} {
		s.calls = 0
		if _, err := a.NativeRead(context.Background(), bad); err == nil || s.calls != 0 {
			t.Fatal("invalid request reached SDK")
		}
	}
	for _, cluster := range []string{`{"cluster_id":"other","region_id":"cn-hangzhou"}`, `{"cluster_id":"c-test","region_id":"cn-shanghai"}`, `{}`} {
		s.cluster = cluster
		s.calls = 0
		if _, err := a.NativeRead(context.Background(), r); err == nil || s.calls != 1 {
			t.Fatal("unverified cluster reached addon API")
		}
	}
	s.cluster = `{"cluster_id":"c-test","region_id":"cn-hangzhou"}`
	for _, addon := range []string{`{"name":"other"}`, `{"name":"metrics-server","config":"invalid-forbidden-secret"}`, `{"name":"metrics-server","config":[]}`, `broken-forbidden-secret`} {
		s.addon = addon
		if _, err := a.NativeRead(context.Background(), r); err == nil || strings.Contains(err.Error(), "forbidden-secret") {
			t.Fatal("invalid response accepted or leaked")
		}
	}
	s.calls = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.NativeRead(ctx, r); err == nil || s.calls != 0 {
		t.Fatal("cancelled request reached SDK")
	}
}
