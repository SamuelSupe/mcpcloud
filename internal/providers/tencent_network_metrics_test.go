package providers

import (
	"context"
	"encoding/json"
	"io"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTencentNetworkMetricStatistics(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRET_ID", "test-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "test-key")
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	start := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Minute)
	a := &tencentAdapter{name: "test", profile: config.Profile{Credential: config.Credential{Source: "env"}, Scopes: config.Scopes{Accounts: []string{"123"}}, Regions: []string{"ap-jakarta"}}}
	for _, tc := range []struct {
		selector, statistic, unit string
		value                     float64
	}{{"QCE/NAT_GATEWAY::OutBandwidth?natId=nat-test", "ProviderDefault", "Mbps", 7}, {"QCE/LB_PUBLIC::InTraffic?loadBalancerId=lb-test", "ProviderDefault", "Mbps", 7}, {"QCE/LB_PRIVATE::OutTraffic?loadBalancerId=lb-test", "ProviderDefault", "Mbps", 7}, {"QCE/NAT_GATEWAY::WanInDropPkg?natId=nat-test", "ProviderDefault", "pps", 7}, {"QCE/NAT_GATEWAY::Conns?natId=nat-test", "ProviderDefault", "count", 7}, {"QCE/LB_PUBLIC::ClientConnum?loadBalancerId=lb-test", "ProviderDefault", "count", 7}, {"QCE/COS::StdStorage?bucket=test-12345&appid=12345", "ProviderDefault", "MB", 7}, {"QCE/COS::TotalRequestsPs?bucket=test-12345&appid=12345", "ProviderDefault", "count/s", 7}, {"QCE/LB::VipIntraffic?eip=192.0.2.1", "ProviderDefault", "Mbps", 7}, {"QCE/LB::VipOuttraffic?eip=192.0.2.1", "ProviderDefault", "Mbps", 7}, {"QCE/CVM::CpuUsage?InstanceId=ins-test", "Average", "%", 2}} {
		http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
			var params map[string]any
			if json.NewDecoder(r.Body).Decode(&params) != nil {
				t.Fatal("bad request")
			}
			_, specified := params["SpecifyStatistics"]
			if specified != (tc.statistic == "Average") {
				t.Fatal("incorrect statistics request")
			}
			data := map[string]any{"Response": map[string]any{"DataPoints": []any{map[string]any{"Dimensions": []any{}, "Timestamps": []int64{start.Unix(), end.Unix()}, "Values": []int{7, 99}, "AvgValues": []int{2, 88}}}}}
			b, _ := json.Marshal(data)
			body := string(b)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		p, err := a.queryMetrics(context.Background(), provider.QueryRequest{Source: model.SourceMetrics, Region: "ap-jakarta", Metrics: []string{tc.selector}, Start: &start, End: &end, Step: time.Minute, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Rows) != 1 || p.Rows[0]["value"] != tc.value || p.Rows[0]["unit"] != tc.unit || p.Rows[0]["native"].(map[string]any)["statistic"] != tc.statistic {
			t.Fatalf("wrong result: %+v", p.Rows)
		}
	}
	if tencentMetricUnit("QCE/LB_PUBLIC", "unknown") != "" {
		t.Fatal("unknown unit guessed")
	}
}
