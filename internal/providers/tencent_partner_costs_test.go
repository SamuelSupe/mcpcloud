package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"testing"
	"time"
)

func TestPartnerCostPaginationAndExactAmount(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	c := tencentPartnerCostCursor{Profile: "test", Account: "123", Start: "2026-08-01", End: "2026-09-01", Currency: "USD", Page: 1, Limit: 1, Total: -1}
	p, e := a.partnerCostPage([]byte(`{"Response":{"TotalDetail":{"TotalCount":2},"Detail":[{"Code":"cvm","Name":"Compute","ItemDetail":[{"Period":"2026-08-01","Cost":"0.12345678"},{"Period":"2026-08-02","Cost":"-2.00000001"}]}]}}`), c)
	if e != nil || len(p.Rows) != 2 || p.NextToken == "" || p.Requests != 1 || p.Scanned != 1 {
		t.Fatalf("%+v %v", p, e)
	}
	if p.Rows[0]["native"].(map[string]any)["amount_decimal"] != "0.12345678" {
		t.Fatal("decimal lost")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(p.NextToken)
	if json.Unmarshal(raw, &c) != nil || c.Page != 2 || c.Total != 2 {
		t.Fatal("bad cursor")
	}
	p, e = a.partnerCostPage([]byte(`{"Response":{"TotalDetail":{"TotalCount":2},"Detail":[{"Code":"cos","ItemDetail":[{"Period":"2026-08-01","Cost":"1"}]}]}}`), c)
	if e != nil || p.NextToken != "" || len(p.Rows) != 1 {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestPartnerCostRejectsInvalidResponses(t *testing.T) {
	a := &tencentAdapter{name: "test"}
	c := tencentPartnerCostCursor{Start: "2026-08-01", End: "2026-09-01", Page: 1, Limit: 1, Total: -1}
	for _, b := range []string{`{}`, `{"Response":{}}`, `{"Response":{"Error":{"Code":"Denied"}}}`, `{"Response":{"TotalDetail":{"TotalCount":1},"Detail":[]}}`, `{"Response":{"TotalDetail":{"TotalCount":-1},"Detail":[]}}`, `{"Response":{"TotalDetail":{"TotalCount":1},"Detail":[{"Code":"x","ItemDetail":[{"Period":"2026-08-01","Cost":"NaN"}]}]}}`, `{"Response":{"TotalDetail":{"TotalCount":1},"Detail":[{"Code":"x","ItemDetail":[{"Period":"2026-09-01","Cost":"1"}]}]}}`, `{"Response":{"TotalDetail":{"TotalCount":1},"Detail":[{"Code":"x","ItemDetail":[{"Period":"2026-08-01","Cost":"1"},{"Period":"2026-08-01","Cost":"2"}]}]}}`} {
		if _, e := a.partnerCostPage([]byte(b), c); e == nil {
			t.Fatal("accepted " + b)
		}
	}
	c.Total = 2
	if _, e := a.partnerCostPage([]byte(`{"Response":{"TotalDetail":{"TotalCount":0},"Detail":[]}}`), c); e == nil {
		t.Fatal("changed total")
	}
}

func TestPartnerCostScopeAndCursorGuards(t *testing.T) {
	a := &tencentAdapter{name: "test", profile: config.Profile{Options: map[string]string{"billing_currency": "USD"}, Scopes: config.Scopes{Accounts: []string{"123"}}}}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	c := tencentPartnerCostCursor{Profile: "test", Account: "123", Start: "2026-08-01", End: "2026-09-01", Currency: "USD", Page: 2, Limit: 1, Total: 2}
	for _, field := range []string{"profile", "account", "start", "end", "currency", "page", "limit", "total"} {
		bad := c
		switch field {
		case "profile":
			bad.Profile = "other"
		case "account":
			bad.Account = "other"
		case "start":
			bad.Start = "2026-07-01"
		case "end":
			bad.End = "2026-10-01"
		case "currency":
			bad.Currency = "CNY"
		case "page":
			bad.Page = -1
		case "limit":
			bad.Limit = 0
		case "total":
			bad.Total = 1
		}
		raw, _ := json.Marshal(bad)
		_, e := a.queryPartnerCosts(context.Background(), provider.QueryRequest{Start: &start, End: &end, PageToken: base64.RawURLEncoding.EncodeToString(raw)})
		if e == nil || e.(*provider.Error).Code != "invalid_cursor" {
			t.Fatalf("%s: %v", field, e)
		}
	}
	_, e := a.queryPartnerCosts(context.Background(), provider.QueryRequest{Start: &start, End: &end, Accounts: []string{"other"}})
	if e == nil {
		t.Fatal("outside account accepted")
	}
	a.profile.Options["billing_currency"] = "CNY"
	_, e = a.queryPartnerCosts(context.Background(), provider.QueryRequest{Start: &start, End: &end})
	if e == nil {
		t.Fatal("CNY accepted")
	}
}
