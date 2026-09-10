package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestTencentCLBListPages(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentCLBListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: tencentCLBListOperationName}
	first, err := a.tencentCLBListPage([]byte(`{"Response":{"TotalCount":2,"LoadBalancerSet":[{"LoadBalancerId":"lb-a","Status":1,"LoadBalancerVips":["secret-address"],"NetworkAttributes":{"InternetMaxBandwidthOut":20,"Other":"secret-value"}}]}}`), scope, 1)
	if err != nil || len(first.Rows) != 1 || first.NextToken == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	raw, _ := json.Marshal(first.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("unexpected field leakage")
	}
	next, err := decodeTencentCLBListCursor(first.NextToken, scope)
	if err != nil || next.Offset != 1 || next.Total != 2 {
		t.Fatalf("cursor: %+v %v", next, err)
	}
	last, err := a.tencentCLBListPage([]byte(`{"Response":{"TotalCount":2,"LoadBalancerSet":[{"LoadBalancerId":"lb-b"}]}}`), next, 1)
	if err != nil || len(last.Rows) != 1 || last.NextToken != "" || last.Rows[0]["id"] != "lb-b" {
		t.Fatalf("last page: %+v %v", last, err)
	}
	empty, err := a.tencentCLBListPage([]byte(`{"Response":{"TotalCount":0,"LoadBalancerSet":[]}}`), scope, 1)
	if err != nil || len(empty.Rows) != 0 || empty.NextToken != "" {
		t.Fatal("valid empty list rejected")
	}
	if _, err := a.tencentCLBListPage([]byte(`{"Response":{"TotalCount":3,"LoadBalancerSet":[{"LoadBalancerId":"lb-b"}]}}`), next, 1); err == nil {
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
		if _, err := decodeTencentCLBListCursor(first.NextToken, changed); err == nil {
			t.Fatal("scope change accepted: " + field)
		}
	}
}
func TestTencentCLBListInvalidResponses(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentCLBListCursor{Operation: tencentCLBListOperationName}
	for _, body := range []string{`{}`, `{"Response":null}`, `{"Response":{}}`, `{"Response":{"TotalCount":0,"LoadBalancerSet":null}}`, `{"Response":{"TotalCount":-1,"LoadBalancerSet":[]}}`, `{"Response":{"TotalCount":1,"LoadBalancerSet":[]}}`, `{"Response":{"TotalCount":0,"LoadBalancerSet":[{"LoadBalancerId":"lb-a"}]}}`, `{"Response":{"TotalCount":1,"LoadBalancerSet":[{}]}}`, `{"Response":{"TotalCount":2,"LoadBalancerSet":[{"LoadBalancerId":"lb-a"},{"LoadBalancerId":"lb-a"}]}}`, `{"Response":{"Error":{"Code":"UnauthorizedOperation","Message":"secret"}}}`} {
		_, err := a.tencentCLBListPage([]byte(body), scope, 100)
		if err == nil {
			t.Fatal("invalid response accepted: " + body)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("raw provider message exposed")
		}
	}
	for _, token := range []string{"invalid", base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":-1}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":1,"Total":1}`))} {
		if _, err := decodeTencentCLBListCursor(token, scope); err == nil {
			t.Fatal("bad cursor accepted")
		}
	}
	if _, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: tencentCLBListOperationName, Params: map[string]any{"password": "secret"}}); err == nil {
		t.Fatal("unknown parameter accepted")
	}
}
