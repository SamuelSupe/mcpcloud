package providers

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestParseMetricSelectorRejectsAmbiguousOrInjectionLikeDimensions(t *testing.T) {
	tooManyDimensions := make([]string, 0, 17)
	for i := 0; i < 17; i++ {
		tooManyDimensions = append(tooManyDimensions, fmt.Sprintf("resource.id%d=value", i))
	}
	tests := []struct {
		name string
		raw  string
	}{
		{name: "malformed percent escape", raw: "namespace::metric?resource.id=%ZZ"},
		{name: "newline in dimension value", raw: "namespace::metric?resource.id=value%0Aother"},
		{name: "duplicate dimension", raw: "namespace::metric?resource.id=one&resource.id=two"},
		{name: "empty dimension value", raw: "namespace::metric?resource.id="},
		{name: "too many dimensions", raw: "namespace::metric?" + strings.Join(tooManyDimensions, "&")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parseMetricSelector(tt.raw, 2, 2); err == nil {
				t.Fatalf("parseMetricSelector(%q) accepted an unsafe or ambiguous selector", tt.raw)
			}
		})
	}

	queryLike, err := parseMetricSelector("namespace::metric?resource.id=%22%20OR%201%3D1%3BResources", 2, 2)
	if err != nil {
		t.Fatalf("parseMetricSelector() rejected a bounded literal dimension value: %v", err)
	}
	if got, want := queryLike.Dimensions["resource.id"], `" OR 1=1;Resources`; got != want {
		t.Fatalf("parsed query-like dimension = %q, want literal %q", got, want)
	}

	selector, err := parseMetricSelector("namespace::metric?resource.id=i-123&metric.state=running", 2, 2)
	if err != nil {
		t.Fatalf("parseMetricSelector() rejected a valid scoped selector: %v", err)
	}
	if selector.Dimensions["resource.id"] != "i-123" || selector.Dimensions["metric.state"] != "running" {
		t.Fatalf("parsed dimensions = %#v, want both validated dimensions", selector.Dimensions)
	}
}

func TestGCPMetricFilterRejectsUnscopedDimensionNames(t *testing.T) {
	for _, name := range []string{"label", "other.id", "metric.", "metric.label.with.dot", "resource.label\n"} {
		selector := metricSelector{
			Parts:      []string{"compute.googleapis.com/instance/cpu/utilization"},
			Dimensions: map[string]string{name: "value"},
		}
		if _, err := gcpMetricFilter(selector); err == nil {
			t.Errorf("gcpMetricFilter() accepted unscoped or malformed dimension %q", name)
		}
	}

	selector := metricSelector{
		Parts:      []string{"compute.googleapis.com/instance/cpu/utilization"},
		Dimensions: map[string]string{"resource.instance_id": "i-123"},
	}
	filter, err := gcpMetricFilter(selector)
	if err != nil {
		t.Fatalf("gcpMetricFilter() rejected a valid resource dimension: %v", err)
	}
	if !strings.Contains(filter, `resource.labels."instance_id" = "i-123"`) {
		t.Fatalf("gcpMetricFilter() = %q, want resource label constraint", filter)
	}
}

func TestProviderCursorsRejectMalformedAndNegativeState(t *testing.T) {
	periodTests := []struct {
		name  string
		token string
	}{
		{name: "malformed base64", token: "not-a-cursor"},
		{name: "negative offset", token: encodePeriodCursor(periodCursor{Offset: -1})},
		{name: "negative page", token: encodePeriodCursor(periodCursor{Page: -1})},
		{name: "oversized payload", token: base64.RawURLEncoding.EncodeToString([]byte(`{"p":"` + strings.Repeat("x", 260) + `"}`))},
	}
	for _, tt := range periodTests {
		t.Run("period/"+tt.name, func(t *testing.T) {
			if _, err := decodePeriodCursor(tt.token); err == nil {
				t.Fatalf("decodePeriodCursor() accepted %s", tt.name)
			}
		})
	}

	valid := encodePeriodCursor(periodCursor{Period: "2026-08", Offset: 10, Page: 2})
	if cursor, err := decodePeriodCursor(valid); err != nil || cursor.Period != "2026-08" || cursor.Offset != 10 || cursor.Page != 2 {
		t.Fatalf("decodePeriodCursor(valid) = %#v, %v", cursor, err)
	}

	bigQueryTests := []struct {
		name  string
		value bigQueryCursor
	}{
		{name: "path traversal job id", value: bigQueryCursor{JobID: "../../jobs/1"}},
		{name: "invalid location", value: bigQueryCursor{JobID: "job-1", Location: "us west1"}},
	}
	for _, tt := range bigQueryTests {
		t.Run("bigquery/"+tt.name, func(t *testing.T) {
			if _, err := decodeBigQueryCursor(encodeBigQueryCursor(tt.value)); err == nil {
				t.Fatalf("decodeBigQueryCursor() accepted %s", tt.name)
			}
		})
	}

	if cursor, err := decodeBigQueryCursor(encodeBigQueryCursor(bigQueryCursor{JobID: "job-1", Location: "US", PageToken: "opaque"})); err != nil || cursor.JobID != "job-1" {
		t.Fatalf("decodeBigQueryCursor(valid) = %#v, %v", cursor, err)
	}
}

func TestAzureCostNextLinkIsPinnedToTheExpectedReadEndpoint(t *testing.T) {
	const subscription = "sub-123"
	valid := "https://management.azure.com/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query?$skiptoken=opaque-token"
	if token, err := azureCostSkipToken(valid, subscription); err != nil || token != "opaque-token" {
		t.Fatalf("azureCostSkipToken(valid) = %q, %v", token, err)
	}

	unsafe := []string{
		"http://management.azure.com/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query?$skiptoken=opaque-token",
		"https://evil.example/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query?$skiptoken=opaque-token",
		"https://management.azure.com/subscriptions/other/providers/Microsoft.CostManagement/query?$skiptoken=opaque-token",
		"https://user:pass@management.azure.com/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query?$skiptoken=opaque-token",
		"https://management.azure.com/subscriptions/" + subscription + "/providers/Microsoft.CostManagement/query",
	}
	for _, nextLink := range unsafe {
		if _, err := azureCostSkipToken(nextLink, subscription); err == nil {
			t.Errorf("azureCostSkipToken() accepted unsafe next link %q", nextLink)
		}
	}
}
