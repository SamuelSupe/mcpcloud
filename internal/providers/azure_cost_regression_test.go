package providers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAzureCostCurrencyIsResponseColumn(t *testing.T) {
	// The live subscription rejects Currency as a grouping dimension (HTTP 400).
	raw, _ := json.Marshal(azureCostQueryBody(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)))
	if strings.Contains(string(raw), `"Currency"`) || !strings.Contains(string(raw), `"ServiceName"`) {
		t.Fatalf("unsupported grouping: %s", raw)
	}
	page, err := decodeAzureCostPage(strings.NewReader(`{"properties":{"columns":[{"name":"Currency"},{"name":"ServiceName"},{"name":"UsageDate"},{"name":"Cost"}],"rows":[["USD","Storage",20261009,0.125]]}}`), "sub", "profile")
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("decode: %+v %v", page, err)
	}
	row := page.Rows[0]
	if row["currency"] != "USD" || row["amount"] != 0.125 || row["date"] != "2026-10-09" {
		t.Fatalf("cost column mapping: %#v", row)
	}
}
