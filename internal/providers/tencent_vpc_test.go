package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTencentVPCDetailsExcludeAddressesAndRules(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	var d map[string]any
	if err := json.Unmarshal([]byte(`{"NatGatewayId":"nat-test","State":"AVAILABLE","MaxConcurrentConnection":3000000,"PublicIpAddressSet":[{"AddressId":"eip-test","PublicIpAddress":"secret-ip"}],"SourceIpTranslationNatRuleSet":[{"PrivateIp":"secret-private"}],"FailureInfo":"secret-error"}`), &d); err != nil {
		t.Fatal(err)
	}
	row := a.tencentVPCRow(d, tencentVPCSpecs["tencent.vpc.describe_nat_gateway"], "ap-jakarta", "123")
	raw, _ := json.Marshal(row)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("sensitive data leaked")
	}
	if !strings.Contains(string(raw), "eip-test") || row["state"] != "available" {
		t.Fatal("missing binding or state")
	}
	if err := json.Unmarshal([]byte(`{"AddressId":"eip-test","AddressStatus":"BIND","InstanceId":"nat-test","Bandwidth":1600,"AddressIp":"secret-public","PrivateAddressIp":"secret-private"}`), &d); err != nil {
		t.Fatal(err)
	}
	row = a.tencentVPCRow(d, tencentVPCSpecs["tencent.vpc.describe_address"], "ap-jakarta", "123")
	raw, _ = json.Marshal(row)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("EIP address leaked")
	}
	attrs := row["attributes"].(map[string]any)
	if attrs["bound_resource_id"] != "nat-test" || attrs["bandwidth_limit"] != float64(1600) {
		t.Fatal("incorrect EIP metadata")
	}
}
