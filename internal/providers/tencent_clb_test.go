package providers

import (
	"context"
	"encoding/json"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestTencentCLBDetailSafeFields(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	var d map[string]any
	if err := json.Unmarshal([]byte(`{"LoadBalancerId":"lb-test","Status":1,"VpcId":"vpc-test","NetworkAttributes":{"InternetMaxBandwidthOut":2000,"InternetChargeType":"TRAFFIC_POSTPAID_BY_HOUR","Unexpected":"secret-nested"},"Tags":[{"TagKey":"env","TagValue":"prod"},{"TagKey":"","TagValue":"bad"}],"LoadBalancerVips":["secret-ip"],"LoadBalancerDomain":"secret-domain","Targets":[{"Password":"secret-password"}]}`), &d); err != nil {
		t.Fatal(err)
	}
	row := a.tencentCLBRow(d, "ap-jakarta", "123")
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("sensitive fields leaked")
	}
	if row["state"] != "running" || row["attributes"].(map[string]any)["max_outbound_bandwidth_mbps"] != float64(2000) || row["tags"].(map[string]any)["env"] != "prod" {
		t.Fatal("incorrect mapping")
	}
	if _, ok := row["tags"].(map[string]any)[""]; ok {
		t.Fatal("empty tag")
	}
	delete(d, "Status")
	delete(d, "NetworkAttributes")
	row = a.tencentCLBRow(d, "ap-jakarta", "123")
	if row["state"] != "unknown" {
		t.Fatal("missing state guessed")
	}
	if _, ok := row["attributes"].(map[string]any)["max_outbound_bandwidth_mbps"]; ok {
		t.Fatal("missing bandwidth guessed")
	}
	d["Status"] = float64(0)
	if a.tencentCLBRow(d, "ap-jakarta", "123")["state"] != "creating" {
		t.Fatal("creating state")
	}
	d["Status"] = float64(99)
	if a.tencentCLBRow(d, "ap-jakarta", "123")["state"] != "unknown" {
		t.Fatal("unknown state")
	}
}
func TestTencentCLBRejectsInvalidRequests(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	for _, req := range []provider.NativeRequest{
		{Operation: tencentCLBOperationName, Params: map[string]any{}},
		{Operation: tencentCLBOperationName, Params: map[string]any{"load_balancer_id": "lb-test", "password": "secret"}},
		{Operation: tencentCLBOperationName, Params: map[string]any{"load_balancer_id": "lb-test"}, PageToken: "other"},
	} {
		if _, err := a.NativeRead(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
