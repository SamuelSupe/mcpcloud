package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTencentNodesPaginationAndSensitiveFieldIsolation(t *testing.T) {
	var p tencentNodeResponse
	err := json.Unmarshal([]byte(`{"Response":{"TotalCount":2,"InstanceSet":[{"InstanceId":"np-test-1","InstanceState":"running","NodeType":"Native","NodePoolId":"np-test","LanIP":"sensitive-address","Native":{"UserScript":"secret-script"},"FailedReason":"secret-error"}]}}`), &p)
	if err != nil {
		t.Fatal(err)
	}
	a := &tencentAdapter{name: "test"}
	page, err := a.tencentNodePage(p, "cls-test", "ap-jakarta", "123", 0)
	if err != nil {
		t.Fatal(err)
	}
	offset, cursorErr := decodeTencentNodeCursor(page.NextToken, "cls-test", "ap-jakarta", "123")
	if cursorErr != nil || offset != 1 || len(page.Rows) != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
	for _, scope := range [][3]string{{"cls-other", "ap-jakarta", "123"}, {"cls-test", "ap-shanghai", "123"}, {"cls-test", "ap-jakarta", "456"}} {
		if _, err := decodeTencentNodeCursor(page.NextToken, scope[0], scope[1], scope[2]); err == nil {
			t.Fatal("cross-scope cursor accepted")
		}
	}
	body, _ := json.Marshal(page.Rows)
	for _, secret := range []string{"sensitive-address", "secret-script", "secret-error", "LanIP", "UserScript"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("leaked field %s", secret)
		}
	}
	page, err = a.tencentNodePage(p, "cls-test", "ap-jakarta", "123", 1)
	if err != nil || page.NextToken != "" {
		t.Fatalf("final page: %+v %v", page, err)
	}
	p.Response.Errors = []string{"sensitive-upstream-error"}
	_, err = a.tencentNodePage(p, "cls-test", "ap-jakarta", "123", 0)
	if err == nil || strings.Contains(err.Error(), "sensitive-upstream-error") {
		t.Fatalf("unsafe partial response: %v", err)
	}
	p.Response.Errors = nil
	p.Response.InstanceSet = nil
	if _, err = a.tencentNodePage(p, "cls-test", "ap-jakarta", "123", 0); err == nil {
		t.Fatal("empty incomplete page accepted")
	}
}
