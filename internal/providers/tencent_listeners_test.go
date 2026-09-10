package providers

import (
	"encoding/json"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestTencentListenerPagingAndIsolation(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	var rows []map[string]any
	err := json.Unmarshal([]byte(`[{"ListenerId":"lbl-b","ListenerName":"B","Protocol":"HTTPS","Port":443,"Certificate":{"Key":"secret-cert"},"Rules":[{"Domain":"secret-domain"}]},{"ListenerId":"lbl-a","Protocol":"HTTP","Port":80,"Scheduler":null}]`), &rows)
	if err != nil {
		t.Fatal(err)
	}
	req := provider.NativeRequest{Operation: tencentListenersOperationName, Limit: 1}
	p, err := a.tencentListenerPage(rows, req, "lb-test", "ap-jakarta", "123")
	if err != nil || len(p.Rows) != 1 || p.Rows[0]["id"] != "lbl-a" || p.NextToken == "" {
		t.Fatalf("first page: %+v %v", p, err)
	}
	req.PageToken = p.NextToken
	p, err = a.tencentListenerPage(rows, req, "lb-test", "ap-jakarta", "123")
	if err != nil || len(p.Rows) != 1 || p.Rows[0]["id"] != "lbl-b" || p.NextToken != "" {
		t.Fatalf("last page: %+v %v", p, err)
	}
	raw, _ := json.Marshal(p.Rows)
	if strings.Contains(string(raw), "secret-") {
		t.Fatal("sensitive data leaked")
	}
	attrs := p.Rows[0]["attributes"].(map[string]any)
	if attrs["port"] != float64(443) || attrs["load_balancer_id"] != "lb-test" {
		t.Fatal("metadata missing")
	}
	for _, s := range []tencentListenerCursor{{Profile: "other", Account: "123", Region: "ap-jakarta", LoadBalancer: "lb-test"}, {Profile: "test", Account: "other", Region: "ap-jakarta", LoadBalancer: "lb-test"}, {Profile: "test", Account: "123", Region: "other", LoadBalancer: "lb-test"}, {Profile: "test", Account: "123", Region: "ap-jakarta", LoadBalancer: "other"}} {
		if _, err := decodeTencentListenerCursor(req.PageToken, s); err == nil {
			t.Fatal("cross-scope cursor accepted")
		}
	}
	if _, err := decodeTencentListenerCursor("bad", tencentListenerCursor{}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	req.PageToken = ""
	p, err = a.tencentListenerPage(nil, req, "lb-test", "ap-jakarta", "123")
	if err != nil || len(p.Rows) != 0 || p.NextToken != "" {
		t.Fatal("empty page")
	}
	if _, err := a.tencentListenerPage([]map[string]any{{}}, req, "lb-test", "ap-jakarta", "123"); err == nil {
		t.Fatal("missing id accepted")
	}
	if _, err := a.tencentListenerPage([]map[string]any{rows[0], rows[0]}, req, "lb-test", "ap-jakarta", "123"); err == nil {
		t.Fatal("duplicate id accepted")
	}
}
