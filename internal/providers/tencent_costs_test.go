package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"mcpcloud/internal/provider"
)

func TestTencentCostsAllowChangingTotals(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRET_ID", "test")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "test")
	t.Setenv("TENCENTCLOUD_TOKEN", "")
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	total := 2
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "billing.tencentcloudapi.com" {
			return nil, fmt.Errorf("unexpected host: %s", r.URL.Host)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["PageSize"] != float64(100) || body["PageNo"] != float64(1) {
			return nil, fmt.Errorf("unstable provider page: %v", body)
		}
		details := make([]string, total)
		for i := range details {
			details[i] = fmt.Sprintf(`{"Name":"product-%d","TimeDetail":[{"Time":"2026-09-01","Money":"%d"}]}`, i, i+1)
		}
		return providerTestHTTPResponse(r, fmt.Sprintf(`{"Response":{"Total":%d,"Detail":[%s]}}`, total, strings.Join(details, ","))), nil
	})
	a := &tencentAdapter{name: "costs", profile: envCredentialProfile(nil)}
	a.profile.Scopes.Accounts = []string{"123"}
	a.profile.Options = map[string]string{"billing_currency": "CNY"}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	for _, updatedTotal := range []int{3, 1, 0} {
		total = 2
		req := provider.QueryRequest{Start: &start, End: &end, Limit: 1}
		page, err := a.queryCosts(context.Background(), req)
		if err != nil || len(page.Rows) != 1 || page.NextToken == "" {
			t.Fatalf("first page: %+v %v", page, err)
		}
		total = updatedTotal
		req.PageToken = page.NextToken
		req.Limit = 100
		page, err = a.queryCosts(context.Background(), req)
		if err != nil || len(page.Rows) != max(updatedTotal-1, 0) || page.NextToken != "" {
			t.Fatalf("total %d: %+v %v", updatedTotal, page, err)
		}
	}
}
