package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestTencentProductListPages(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentProductListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: "tencent.cvm.list_instances"}
	first, err := a.tencentProductListPage([]byte(`{"Response":{"TotalCount":2,"InstanceSet":[{"InstanceId":"lb-a","Status":1,"LoadBalancerVips":["secret-address"],"NetworkAttributes":{"InternetMaxBandwidthOut":20,"Other":"secret-value"}}]}}`), scope, 1)
	if err != nil || len(first.Rows) != 1 || first.NextToken == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	raw, _ := json.Marshal(first.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("unexpected field leakage")
	}
	next, err := decodeTencentProductListCursor(first.NextToken, scope)
	if err != nil || next.Offset != 1 || next.Total != 2 {
		t.Fatalf("cursor: %+v %v", next, err)
	}
	last, err := a.tencentProductListPage([]byte(`{"Response":{"TotalCount":2,"InstanceSet":[{"InstanceId":"lb-b"}]}}`), next, 1)
	if err != nil || len(last.Rows) != 1 || last.NextToken != "" || last.Rows[0]["id"] != "lb-b" {
		t.Fatalf("last page: %+v %v", last, err)
	}
	empty, err := a.tencentProductListPage([]byte(`{"Response":{"TotalCount":0,"InstanceSet":[]}}`), scope, 1)
	if err != nil || len(empty.Rows) != 0 || empty.NextToken != "" {
		t.Fatal("valid empty list rejected")
	}
	if _, err := a.tencentProductListPage([]byte(`{"Response":{"TotalCount":3,"InstanceSet":[{"InstanceId":"lb-b"}]}}`), next, 1); err == nil {
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
		if _, err := decodeTencentProductListCursor(first.NextToken, changed); err == nil {
			t.Fatal("scope change accepted: " + field)
		}
	}
}
func TestTencentProductListInvalidResponses(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentProductListCursor{Operation: "tencent.cvm.list_instances"}
	for _, body := range []string{`{}`, `{"Response":null}`, `{"Response":{}}`, `{"Response":{"TotalCount":0,"InstanceSet":null}}`, `{"Response":{"TotalCount":-1,"InstanceSet":[]}}`, `{"Response":{"TotalCount":1,"InstanceSet":[]}}`, `{"Response":{"TotalCount":0,"InstanceSet":[{"InstanceId":"lb-a"}]}}`, `{"Response":{"TotalCount":1,"InstanceSet":[{}]}}`, `{"Response":{"TotalCount":2,"InstanceSet":[{"InstanceId":"lb-a"},{"InstanceId":"lb-a"}]}}`, `{"Response":{"Error":{"Code":"UnauthorizedOperation","Message":"secret"}}}`} {
		_, err := a.tencentProductListPage([]byte(body), scope, 100)
		if err == nil {
			t.Fatal("invalid response accepted: " + body)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("raw provider message exposed")
		}
	}
	for _, token := range []string{"invalid", base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":-1}`)), base64.RawURLEncoding.EncodeToString([]byte(`{"Offset":1,"Total":1}`))} {
		if _, err := decodeTencentProductListCursor(token, scope); err == nil {
			t.Fatal("bad cursor accepted")
		}
	}
	if _, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: "tencent.cvm.list_instances", Params: map[string]any{"password": "secret"}}); err == nil {
		t.Fatal("unknown parameter accepted")
	}
}
func TestTencentProductListMappings(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	for op, s := range tencentProductListSpecs {
		scope := tencentProductListCursor{Profile: "test", Account: "123", Region: "ap-jakarta", Operation: op}
		body, _ := json.Marshal(map[string]any{"Response": map[string]any{"TotalCount": 1, s.list: []any{map[string]any{s.id: "resource-a", "Password": "secret-password", "ConnectionString": "secret-connection"}}}})
		p, err := a.tencentProductListPage(body, scope, 1)
		if err != nil || len(p.Rows) != 1 || p.Rows[0]["id"] != "resource-a" {
			t.Fatalf("%s: %+v %v", op, p, err)
		}
		raw, _ := json.Marshal(p.Rows)
		if strings.Contains(string(raw), "secret-") {
			t.Fatal("unapproved fields leaked")
		}
		scope.Offset = 1
		scope.Total = 2
		token, _ := json.Marshal(scope)
		for other := range tencentProductListSpecs {
			if other == op {
				continue
			}
			changed := scope
			changed.Operation = other
			if _, err := decodeTencentProductListCursor(base64.RawURLEncoding.EncodeToString(token), changed); err == nil {
				t.Fatal("cross-product cursor accepted")
			}
		}
	}
	scope := tencentProductListCursor{Operation: "tencent.cvm.list_instances"}
	if _, err := a.tencentProductListPage([]byte(`{"Response":{"TotalCount":1,"InstanceSet":[{"InstanceId":"ins-a","CPU":"invalid"}]}}`), scope, 1); err == nil {
		t.Fatal("invalid typed CPU accepted")
	}
}
