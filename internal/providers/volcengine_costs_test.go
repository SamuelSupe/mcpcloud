package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestVolcengineCostsRequestDailyBillsAndNormalizeRegions(t *testing.T) {
	t.Setenv("MCP_TEST_VOLC_ACCESS", "access")
	t.Setenv("MCP_TEST_VOLC_SECRET", "secret")
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	http.DefaultClient = &http.Client{Transport: providerTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["GroupPeriod"] != float64(1) || body["BillPeriod"] != "2026-09" || body["Offset"] != float64(0) {
			t.Errorf("unexpected billing parameters: %v", body)
		}
		return providerTestHTTPResponse(req, `{"Result":{"Total":3,"List":[{"ExpenseDate":"2026-09-05","OwnerID":"2118159236","RegionCode":"R000310","Region":"华东2（上海）","PretaxAmount":"12.50","Currency":"CNY","Product":"ECS","BillDetailId":"daily-1"},{"ExpenseDate":"2026-09-01","OwnerID":"2118159236","RegionCode":"R000305","PretaxAmount":"9","Currency":"CNY","Product":"ECS","BillDetailId":"outside-range"}]}}`), nil
	})}
	adapter := &volcengineAdapter{name: "test", profile: config.Profile{Credential: config.Credential{Source: "env", Env: map[string]string{"VOLCENGINE_ACCESS_KEY_ID": "MCP_TEST_VOLC_ACCESS", "VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_SECRET"}}, Regions: []string{"cn-beijing", "cn-shanghai"}}}
	start := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	page, err := adapter.Query(context.Background(), provider.QueryRequest{Source: model.SourceCosts, Accounts: []string{"2118159236"}, Start: &start, End: &end, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || page.Rows[0]["region"] != "cn-shanghai" || page.Rows[0]["amount"] != 12.5 || page.Rows[0]["date"] != "2026-09-05" {
		t.Fatalf("wrong normalized bill: %+v", page)
	}
	cursor, err := decodePeriodCursor(page.NextToken)
	if err != nil || cursor.Offset != 2 || cursor.Period != "2026-09" || page.Scanned != 2 {
		t.Fatalf("wrong pagination: %+v, %v", page, err)
	}
}

func TestVolcengineBillDateRejectsMonthlyOrMissingDates(t *testing.T) {
	for _, value := range []string{"", "2026-09", "invalid"} {
		if _, _, err := volcengineBillDate(value, value); err == nil {
			t.Errorf("accepted non-daily date %q", value)
		}
	}
	for _, value := range []string{"2026-09-05", "2026/9/5", "2026-09-05 12:00:00", "2026-09-05T12:00:00Z"} {
		date, _, err := volcengineBillDate(value, "")
		if err != nil || date != "2026-09-05" {
			t.Errorf("date %q: %q, %v", value, date, err)
		}
	}
}

func TestVolcengineBillRegionsPreserveUnknownCodes(t *testing.T) {
	for _, tc := range []struct{ code, name, want string }{{"R000305", "华北2（北京）", "cn-beijing"}, {"R000310", "华东2（上海）", "cn-shanghai"}, {"R999999", "unknown", "R999999"}, {"cn-beijing", "", "cn-beijing"}, {"", "cn-shanghai", "cn-shanghai"}} {
		if got := volcengineBillRegion(tc.code, tc.name); got != tc.want {
			t.Errorf("region %q: got %q, want %q", tc.code, got, tc.want)
		}
	}
}
