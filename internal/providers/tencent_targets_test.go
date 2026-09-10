package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

func TestTencentTargetHealthFlatteningAndPagination(t *testing.T) {
	body := []byte(`{"Response":{"LoadBalancers":[{"LoadBalancerId":"lb-test","Listeners":[{"ListenerId":"lbl-test","Protocol":"HTTP","Port":80,"Rules":[{"LocationId":"loc-a","Domain":"secret-domain","Url":"secret-url","Targets":[{"TargetId":"ins-test","IP":"secret-ip","Port":8080,"Weight":0,"HealthStatus":false,"HealthStatusDetail":"Unknown"}]},{"LocationId":"loc-b","Targets":[{"TargetId":"ins-test","Port":8080,"HealthStatus":true,"HealthStatusDetial":"Alive"},{"TargetId":"ins-other","Port":9090,"HealthStatusDetail":"secret-detail"}]}]}]}]}}`)
	listeners, err := parseTencentCLBTargets(body, "lb-test", tencentTargetHealthOperationName)
	if err != nil {
		t.Fatal(err)
	}
	a := &tencentAdapter{name: "test"}
	scope := tencentTargetCursor{Profile: "test", Account: "123", Region: "ap-jakarta", LoadBalancer: "lb-test", Operation: tencentTargetHealthOperationName}
	req := provider.NativeRequest{Operation: scope.Operation, Limit: 1}
	rows := []map[string]any{}
	first := ""
	for i := 0; i < 4; i++ {
		page, err := a.tencentTargetPage(listeners, req, scope)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, page.Rows...)
		if first == "" {
			first = page.NextToken
		}
		req.PageToken = page.NextToken
		if req.PageToken == "" {
			break
		}
	}
	if len(rows) != 3 {
		t.Fatalf("lost bindings: %d", len(rows))
	}
	seen := map[string]bool{}
	states := map[string]int{}
	for _, row := range rows {
		key := row["id"].(string)
		if seen[key] {
			t.Fatal("duplicate key")
		}
		seen[key] = true
		states[row["state"].(string)]++
		attrs := row["attributes"].(map[string]any)
		if row["state"] == "unknown" {
			if _, ok := attrs["healthy"]; ok {
				t.Fatal("missing health became false")
			}
		}
		if attrs["location_id"] == "loc-a" && attrs["weight"] != 0 {
			t.Fatal("zero weight omitted")
		}
	}
	if states["healthy"] != 1 || states["unhealthy"] != 1 || states["unknown"] != 1 {
		t.Fatalf("states: %v", states)
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("sensitive fields leaked")
	}
	for _, field := range []string{"profile", "account", "region", "lb", "op"} {
		s := scope
		switch field {
		case "profile":
			s.Profile = "other"
		case "account":
			s.Account = "other"
		case "region":
			s.Region = "other"
		case "lb":
			s.LoadBalancer = "other"
		case "op":
			s.Operation = tencentTargetsOperationName
		}
		if _, err := decodeTencentTargetCursor(first, s); err == nil {
			t.Fatal("cross-scope cursor accepted")
		}
	}
	if _, err := decodeTencentTargetCursor("bad", scope); err == nil {
		t.Fatal("bad cursor accepted")
	}
}

func TestTencentTargetBindingShapesAndFailures(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	scope := tencentTargetCursor{Profile: "test", Account: "123", Region: "ap-jakarta", LoadBalancer: "lb-test", Operation: tencentTargetsOperationName}
	req := provider.NativeRequest{Operation: scope.Operation, Limit: 100}
	fixtures := []struct {
		body string
		want int
		code string
	}{
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-tcp","Targets":[{"InstanceId":"ins-test","Type":"CVM","Port":80,"Weight":10,"PrivateIpAddresses":["secret-ip"]}]}]}}`, 1, ""},
		{`{"Response":{"Listeners":[]}}`, 0, ""},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Targets":[{"InstanceId":"","Port":80}]}]}}`, 0, "invalid_provider_response"},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Targets":[{"InstanceId":"10.0.0.1","Port":80}]}]}}`, 1, ""},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Targets":[{"InstanceId":"ins-test"}]}]}}`, 0, "invalid_provider_response"},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Targets":[{"InstanceId":"ins-test","Port":80},{"InstanceId":"ins-test","Port":80}]}]}}`, 0, "invalid_provider_response"},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Rules":[{"FunctionTargets":[{}]}]}]}}`, 0, "unsupported_target_type"},
		{`{"Response":{"Listeners":[{"ListenerId":"lbl-test","Rules":[{"PolarisTargets":[{}]}]}]}}`, 0, "unsupported_target_type"},
		{`{"Response":{"Listeners":[{}]}}`, 0, "invalid_provider_response"},
		{`{"Response":{}}`, 0, "invalid_provider_response"},
		{`{"Response":{"Error":{"Code":"UnauthorizedOperation","Message":"secret-error"}}}`, 0, "UnauthorizedOperation"},
	}
	for _, tt := range fixtures {
		ls, err := parseTencentCLBTargets([]byte(tt.body), scope.LoadBalancer, scope.Operation)
		var p provider.Page
		if err == nil {
			p, err = a.tencentTargetPage(ls, req, scope)
		}
		if tt.code != "" {
			e, ok := err.(*provider.Error)
			if !ok || e.Code != tt.code {
				t.Fatalf("want %s got %v", tt.code, err)
			}
			if strings.Contains(err.Error(), "secret-") {
				t.Fatal("error leaked")
			}
			continue
		}
		if err != nil || len(p.Rows) != tt.want {
			t.Fatalf("rows=%d err=%v", len(p.Rows), err)
		}
		raw, _ := json.Marshal(p.Rows)
		if strings.Contains(string(raw), "secret-") {
			t.Fatal("address leaked")
		}
	}
	for _, body := range []string{`{"Response":{"LoadBalancers":[{"LoadBalancerId":"lb-other"}]}}`, `{"Response":{}}`, `{"Response":{"LoadBalancers":[]}}`} {
		if _, err := parseTencentCLBTargets([]byte(body), "lb-test", tencentTargetHealthOperationName); err == nil {
			t.Fatal("bad health response accepted")
		}
	}
}

func TestTencentTargetRequestBoundariesBeforeNetwork(t *testing.T) {
	a := &tencentAdapter{name: "test", profile: config.Profile{Regions: []string{"ap-jakarta", "ap-shanghai"}, Scopes: config.Scopes{Accounts: []string{"123"}}}}
	scope := tencentTargetCursor{Profile: "test", Account: "123", Region: "ap-jakarta", LoadBalancer: "lb-test", Operation: tencentTargetsOperationName, LastID: "binding-test"}
	data, _ := json.Marshal(scope)
	token := base64.RawURLEncoding.EncodeToString(data)
	for _, req := range []provider.NativeRequest{
		{Operation: tencentTargetsOperationName, Region: "ap-jakarta", Params: map[string]any{}},
		{Operation: tencentTargetsOperationName, Region: "ap-jakarta", Params: map[string]any{"load_balancer_id": "lb-test", "password": "secret"}},
		{Operation: tencentTargetsOperationName, Region: "eu-frankfurt", Params: map[string]any{"load_balancer_id": "lb-test"}},
		{Operation: tencentTargetsOperationName, Region: "ap-shanghai", Params: map[string]any{"load_balancer_id": "lb-test"}, PageToken: token},
		{Operation: tencentTargetHealthOperationName, Region: "ap-jakarta", Params: map[string]any{"load_balancer_id": "lb-test"}, PageToken: token},
	} {
		if _, err := a.NativeRead(context.Background(), req); err == nil {
			t.Fatal("invalid request accepted")
		} else if strings.Contains(err.Error(), "credential") {
			t.Fatalf("request reached credential loading: %v", err)
		}
	}
}

func TestTencentTargetAddressReference(t *testing.T) {
	scope := tencentTargetCursor{Account: "123", Region: "ap-jakarta", LoadBalancer: "lb-test"}
	id, ref := tencentTargetIdentity(tencentCLBTarget{PrivateIpAddresses: []string{"10.20.30.40"}}, false, scope)
	if id != "" || ref == "" || strings.Contains(ref, "10.20.30.40") {
		t.Fatal("invalid address reference")
	}
	_, healthRef := tencentTargetIdentity(tencentCLBTarget{TargetId: "10.20.30.40", IP: "10.20.30.40"}, true, scope)
	if ref != healthRef {
		t.Fatal("target and health address references differ")
	}
	_, other := tencentTargetIdentity(tencentCLBTarget{PrivateIpAddresses: []string{"10.20.30.41"}}, false, scope)
	if ref == other {
		t.Fatal("addresses collided")
	}
	a := &tencentAdapter{name: "test"}
	port := 80
	p, err := a.tencentTargetPage([]tencentCLBTargetListener{{ListenerId: "lbl-test", Targets: []tencentCLBTarget{{PrivateIpAddresses: []string{"10.20.30.40"}, Port: &port}}}}, provider.NativeRequest{Operation: tencentTargetsOperationName}, scope)
	if err != nil || len(p.Rows) != 1 {
		t.Fatalf("address-only target lost: %v", err)
	}
	raw, _ := json.Marshal(p.Rows)
	if strings.Contains(string(raw), "10.20.30.40") {
		t.Fatal("address leaked")
	}
	if _, ok := p.Rows[0]["attributes"].(map[string]any)["target_id"]; ok {
		t.Fatal("invented instance ID")
	}
}
