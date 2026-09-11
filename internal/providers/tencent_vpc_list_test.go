package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestTencentVPCListPages(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentVPCListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: tencentAddressListOperation}
	first, err := a.tencentVPCListPage([]byte(`{"Response":{"TotalCount":2,"AddressSet":[{"AddressId":"lb-a","Status":1,"AddressIp":["secret-address"],"NetworkAttributes":{"InternetMaxBandwidthOut":20,"Other":"secret-value"}}]}}`), scope, 1)
	if err != nil || len(first.Rows) != 1 || first.NextToken == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	raw, _ := json.Marshal(first.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("unexpected field leakage")
	}
	next, err := decodeTencentVPCListCursor(first.NextToken, scope)
	if err != nil || next.Offset != 1 || next.Total != 2 {
		t.Fatalf("cursor: %+v %v", next, err)
	}
	last, err := a.tencentVPCListPage([]byte(`{"Response":{"TotalCount":2,"AddressSet":[{"AddressId":"lb-b"}]}}`), next, 1)
	if err != nil || len(last.Rows) != 1 || last.NextToken != "" || last.Rows[0]["id"] != "lb-b" {
		t.Fatalf("last page: %+v %v", last, err)
	}
	empty, err := a.tencentVPCListPage([]byte(`{"Response":{"TotalCount":0,"AddressSet":[]}}`), scope, 1)
	if err != nil || len(empty.Rows) != 0 || empty.NextToken != "" {
		t.Fatal("valid empty list rejected")
	}
	if _, err := a.tencentVPCListPage([]byte(`{"Response":{"TotalCount":3,"AddressSet":[{"AddressId":"lb-b"}]}}`), next, 1); err == nil {
		t.Fatal("changing total accepted")
	}
	for _, field := range []string{"profile", "account", "region", "operation"} {
		changed := scope
		switch field {
		case "profile":
			changed.Profile = "other"
		case "account":
			changed.Account = "other"
		case "region":
			changed.Region = "ap-shanghai"
		case "operation":
			changed.Operation = "other"
		}
		if _, err := decodeTencentVPCListCursor(first.NextToken, changed); err == nil {
			t.Fatal("scope change accepted: " + field)
		}
	}
}
func TestTencentVPCListInvalidResponses(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentVPCListCursor{Operation: tencentAddressListOperation}
	for _, body := range []string{`{}`, `{"Response":null}`, `{"Response":{}}`, `{"Response":{"TotalCount":0,"AddressSet":null}}`, `{"Response":{"TotalCount":-1,"AddressSet":[]}}`, `{"Response":{"TotalCount":1,"AddressSet":[]}}`, `{"Response":{"TotalCount":0,"AddressSet":[{"AddressId":"lb-a"}]}}`, `{"Response":{"TotalCount":1,"AddressSet":[{}]}}`, `{"Response":{"TotalCount":2,"AddressSet":[{"AddressId":"lb-a"},{"AddressId":"lb-a"}]}}`, `{"Response":{"Error":{"Code":"UnauthorizedOperation","Message":"secret"}}}`} {
		_, err := a.tencentVPCListPage([]byte(body), scope, 100)
		if err == nil {
			t.Fatal("invalid response accepted: " + body)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("raw provider message exposed")
		}
	}
	for _, token := range []string{"invalid", base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":-1}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":1,"Total":1}`))} {
		if _, err := decodeTencentVPCListCursor(token, scope); err == nil {
			t.Fatal("bad cursor accepted")
		}
	}
	if _, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: tencentAddressListOperation, Params: map[string]any{"password": "secret"}}); err == nil {
		t.Fatal("unknown parameter accepted")
	}
}
func TestTencentNATListProjectionAndCursor(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentVPCListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: tencentNATListOperation}
	body := []byte(`{"Response":{"TotalCount":2,"NatGatewaySet":[{"NatGatewayId":"nat-a","State":"AVAILABLE","VpcId":"vpc-a","PublicIpAddressSet":["secret-ip"],"NatGatewayDestinationIpPortTranslationNatRuleSet":[{"Ip":"secret-rule"}]}]}}`)
	p, err := a.tencentVPCListPage(body, scope, 1)
	if err != nil || len(p.Rows) != 1 || p.NextToken == "" {
		t.Fatalf("NAT page: %+v %v", p, err)
	}
	raw, _ := json.Marshal(p.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("NAT nested fields leaked")
	}
	if p.Rows[0]["id"] != "nat-a" || p.Rows[0]["attributes"].(map[string]any)["vpc_id"] != "vpc-a" {
		t.Fatal("NAT mapping failed")
	}
	scope.Operation = tencentAddressListOperation
	if _, err := decodeTencentVPCListCursor(p.NextToken, scope); err == nil {
		t.Fatal("NAT cursor accepted for EIP")
	}
	if _, err := a.tencentVPCListPage(body, scope, 1); err == nil {
		t.Fatal("NAT response accepted as EIP")
	}
}
