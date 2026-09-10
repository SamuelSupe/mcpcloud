package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTencentNATRulesPaginationAndFiltering(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentNATCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Gateway: "nat-test", Operation: tencentSNATOperation}
	body := []byte(`{"Response":{"TotalCount":2,"SourceIpTranslationNatRuleSet":[{"NatGatewaySnatId":"snat-a","NatGatewayId":"nat-test","VpcId":"vpc-test","ResourceId":"subnet-test","PrivateIpAddress":"secret-ip","PublicIpAddresses":["secret-public"],"Description":"secret-description"}]}}`)
	p, err := a.tencentNATRulePage(body, scope, 1)
	if err != nil || len(p.Rows) != 1 || p.NextToken == "" {
		t.Fatalf("page: %+v %v", p, err)
	}
	raw, _ := json.Marshal(p.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("sensitive data leaked")
	}
	if n, err := decodeTencentNATCursor(p.NextToken, scope); err != nil || n != 1 {
		t.Fatal("offset lost")
	}
	for _, field := range []string{"profile", "account", "region", "gateway", "operation"} {
		s := scope
		switch field {
		case "profile":
			s.Profile = "other"
		case "account":
			s.Account = "other"
		case "region":
			s.Region = "other"
		case "gateway":
			s.Gateway = "other"
		case "operation":
			s.Operation = tencentDNATOperation
		}
		if _, err := decodeTencentNATCursor(p.NextToken, s); err == nil {
			t.Fatal("cross-scope cursor accepted")
		}
	}
	if _, err := decodeTencentNATCursor("bad", scope); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	scope.Offset = 1
	p, err = a.tencentNATRulePage(body, scope, 1)
	if err != nil || p.NextToken != "" {
		t.Fatal("last page incorrect")
	}
	scope.Offset = 0
	for _, b := range []string{`{"Response":{}}`, `{"Response":{"TotalCount":1,"SourceIpTranslationNatRuleSet":[]}}`, `{"Response":{"TotalCount":1,"SourceIpTranslationNatRuleSet":[{"NatGatewayId":"nat-other","NatGatewaySnatId":"snat-a"}]}}`, `{"Response":{"TotalCount":1,"SourceIpTranslationNatRuleSet":[{"NatGatewayId":"nat-test"}]}}`} {
		if _, err := a.tencentNATRulePage([]byte(b), scope, 1); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	p, err = a.tencentNATRulePage([]byte(`{"Response":{"TotalCount":0,"SourceIpTranslationNatRuleSet":null}}`), scope, 1)
	if err != nil || len(p.Rows) != 0 || p.NextToken != "" {
		t.Fatal("empty response")
	}
}
func TestTencentDNATReferences(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentNATCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Gateway: "nat-test", Operation: tencentDNATOperation}
	body := `{"Response":{"TotalCount":1,"NatGatewayDestinationIpPortTranslationNatRuleSet":[{"NatGatewayId":"nat-test","VpcId":"vpc-test","IpProtocol":"TCP","PublicIpAddress":"192.0.2.1","PrivateIpAddress":"10.0.0.1","PublicPort":1234,"PrivatePort":80,"Description":"secret-description"}]}}`
	p, err := a.tencentNATRulePage([]byte(body), scope, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p.Rows)
	for _, v := range []string{"192.0.2.1", "10.0.0.1", "secret-description"} {
		if strings.Contains(string(raw), v) {
			t.Fatal("sensitive value leaked")
		}
	}
	attrs := p.Rows[0]["attributes"].(map[string]any)
	if attrs["public_port"] != 1234 || attrs["private_port"] != 80 || attrs["protocol"] != "TCP" {
		t.Fatal("mapping")
	}
	again, _ := a.tencentNATRulePage([]byte(body), scope, 1)
	if p.Rows[0]["id"] != again.Rows[0]["id"] {
		t.Fatal("unstable reference")
	}
	other, _ := a.tencentNATRulePage([]byte(strings.ReplaceAll(body, "10.0.0.1", "10.0.0.2")), scope, 1)
	if p.Rows[0]["id"] == other.Rows[0]["id"] {
		t.Fatal("different rules collided")
	}
	if _, err := a.tencentNATRulePage([]byte(strings.ReplaceAll(body, `"PublicPort":1234,`, "")), scope, 1); err == nil {
		t.Fatal("missing port accepted")
	}
}
