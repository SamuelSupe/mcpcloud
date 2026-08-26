package dsl

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcpcloud/internal/model"
)

func TestParseBuildsPipelineAndEvaluatesFilter(t *testing.T) {
	now := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	query, err := Parse(`resources
	| scope profile in ("aws-prod", "gcp-prod"), provider in ("aws")
	| where (domain == "compute" and state == "running") and tags["env"] == "prod"
	| include native
		| fields provider, profile, scope.account_id, region, id, tags["env"], native.resource_type
	| sort provider asc, name desc
	| limit 25`, now)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if query.Source != model.SourceResources {
		t.Fatalf("Source = %q, want %q", query.Source, model.SourceResources)
	}
	if got, want := query.Scope.Profiles, []string{"aws-prod", "gcp-prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("profiles = %#v, want %#v", got, want)
	}
	if got, want := query.Scope.Providers, []model.Provider{model.ProviderAWS}; !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %#v, want %#v", got, want)
	}
	if !query.IncludeNative {
		t.Fatal("IncludeNative = false, want true")
	}
	if query.Limit != 25 {
		t.Fatalf("Limit = %d, want 25", query.Limit)
	}
	if got, want := query.Fields, []string{"provider", "profile", "scope.account_id", "region", "id", `tags["env"]`, "native.resource_type"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fields = %#v, want %#v", got, want)
	}
	if got, want := query.Sort, []SortField{{Field: "provider", Direction: "asc"}, {Field: "name", Direction: "desc"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sort = %#v, want %#v", got, want)
	}

	matching := map[string]any{
		"domain": "compute",
		"state":  "running",
		"tags":   map[string]any{"env": "prod"},
	}
	matched, err := query.Filter.Eval(matching)
	if err != nil {
		t.Fatalf("filter Eval() error = %v", err)
	}
	if !matched {
		t.Fatal("filter did not match a row satisfying all predicates")
	}
	matching["state"] = "stopped"
	matched, err = query.Filter.Eval(matching)
	if err != nil {
		t.Fatalf("filter Eval() after mutation error = %v", err)
	}
	if matched {
		t.Fatal("filter matched a row with a non-running state")
	}
	if !strings.Contains(query.FilterText, `tags["env"] == "prod"`) {
		t.Fatalf("FilterText = %q, want the parsed predicate", query.FilterText)
	}
}

func TestParseHandlesQuotedPipesAndTimeStages(t *testing.T) {
	now := time.Date(2026, time.February, 2, 3, 4, 5, 0, time.UTC)
	query, err := Parse(`metrics | where metric == "latency|p95" | range last 2h | step 1m`, now)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if query.Range == nil {
		t.Fatal("Range = nil, want relative range")
	}
	if got, want := query.Range.Start, now.Add(-2*time.Hour); !got.Equal(want) {
		t.Fatalf("range start = %s, want %s", got, want)
	}
	if !query.Range.End.Equal(now) {
		t.Fatalf("range end = %s, want %s", query.Range.End, now)
	}
	if query.Step != time.Minute {
		t.Fatalf("Step = %s, want 1m", query.Step)
	}
	matched, err := query.Filter.Eval(map[string]any{"metric": "latency|p95"})
	if err != nil || !matched {
		t.Fatalf("quoted pipe filter Eval() = (%v, %v), want (true, nil)", matched, err)
	}
}

func TestParseSummarizeAndCostCurrencyGuard(t *testing.T) {
	query, err := Parse(`costs | range "2026-01-01" to "2026-01-31" | summarize sum(amount), count() by currency, provider`, time.Time{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	wantAggregates := []Aggregate{
		{Function: "sum", Field: "amount", Alias: "sum_amount"},
		{Function: "count", Field: "*", Alias: "count"},
	}
	if !reflect.DeepEqual(query.Aggregates, wantAggregates) {
		t.Fatalf("aggregates = %#v, want %#v", query.Aggregates, wantAggregates)
	}
	if got, want := query.GroupBy, []string{"currency", "provider"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group by = %#v, want %#v", got, want)
	}

	if _, err := Parse(`costs | range last 24h | summarize sum(amount) by provider`, time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "group by currency") {
		t.Fatalf("missing currency guard error = %v, want currency grouping rejection", err)
	}
}

func TestParseRejectsUnsupportedStagesAndReadPaths(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "unsupported source", query: "writes | limit 1", want: "unsupported source"},
		{name: "write stage", query: "resources | write secret", want: `unsupported stage "write"`},
		{name: "arbitrary action stage", query: "resources | action ec2:DeleteInstance", want: `unsupported stage "action"`},
		{name: "invalid field", query: "resources | fields secret.value", want: "unsupported field"},
		{name: "native without opt in", query: "resources | fields id, native.instance_type", want: "native fields require"},
		{name: "cross-source field in fields", query: "resources | fields amount", want: `field "amount" is not available for source resources`},
		{name: "cross-source field in where", query: `costs | where metric == "cpu" | range last 1h`, want: `field "metric" is not available for source costs`},
		{name: "non-numeric aggregate field", query: "resources | summarize sum(name)", want: "aggregate sum requires a numeric"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.query, time.Time{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) error = %v, want substring %q", tc.query, err, tc.want)
			}
		})
	}
}

func TestValidateEnforcesSourceSpecificRangesAndSteps(t *testing.T) {
	now := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		query string
		want  string
	}{
		{name: "metrics needs range", query: "metrics | limit 1", want: "metrics queries require a range"},
		{name: "metrics max range", query: `metrics | where metric == "cpu" | range last 768h`, want: "metrics range cannot exceed"},
		{name: "metrics min step", query: `metrics | where metric == "cpu" | range last 1h | step 30s`, want: "metrics step cannot be shorter"},
		{name: "costs needs range", query: "costs | limit 1", want: "costs queries require a range"},
		{name: "resources cannot range", query: "resources | range last 1h", want: "range is only supported"},
		{name: "iam cannot step", query: "iam | step 1m", want: "step is only supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.query, now)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) error = %v, want substring %q", tc.query, err, tc.want)
			}
		})
	}
}

func TestExpressionEvaluationSupportsPathsListsAndShortCircuit(t *testing.T) {
	expr, err := parseExpression(`provider in ("aws", "gcp") and tags["env"] starts_with "prod" and value >= 10`)
	if err != nil {
		t.Fatalf("parseExpression() error = %v", err)
	}
	row := map[string]any{
		"provider": "aws",
		"tags":     map[string]any{"env": "production"},
		"value":    json.Number("10"),
	}
	matched, err := expr.Eval(row)
	if err != nil || !matched {
		t.Fatalf("Eval() = (%v, %v), want (true, nil)", matched, err)
	}

	expr, err = parseExpression(`attributes.owner == "unused" and value > 0`)
	if err != nil {
		t.Fatalf("short-circuit expression parse error = %v", err)
	}
	matched, err = expr.Eval(row)
	if err != nil {
		t.Fatalf("short-circuit Eval() error = %v", err)
	}
	if matched {
		t.Fatal("missing-field conjunction unexpectedly matched")
	}

	expr, err = parseExpression(`not (state == "stopped") or attributes.owner exists`)
	if err != nil {
		t.Fatalf("not/or expression parse error = %v", err)
	}
	matched, err = expr.Eval(map[string]any{"state": "running"})
	if err != nil || !matched {
		t.Fatalf("not/or Eval() = (%v, %v), want (true, nil)", matched, err)
	}
}

func TestExpressionEvaluationRejectsUnsupportedValuesAndOperators(t *testing.T) {
	for _, input := range []string{
		`name = "alice"`,
		`name ==`,
		`name == {"secret":"value"}`,
		`name ~~ "alice"`,
		`name in ("alice",)`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := parseExpression(input); err == nil {
				t.Fatalf("parseExpression(%q) unexpectedly succeeded", input)
			}
		})
	}
}

func TestValueAtAndComparisonsPreserveTypeSafeBehavior(t *testing.T) {
	row := map[string]any{"scope": map[string]any{"account_id": "123"}, "count": int64(2)}
	value, ok := ValueAt(row, "scope.account_id")
	if !ok || value != "123" {
		t.Fatalf("ValueAt() = (%#v, %v), want (123, true)", value, ok)
	}
	if _, ok := ValueAt(row, "scope.missing"); ok {
		t.Fatal("ValueAt() found a missing nested key")
	}

	for _, tc := range []struct {
		actual, op, expected any
		want                 bool
	}{
		{actual: int64(2), op: ">", expected: 1, want: true},
		{actual: float64(2), op: "==", expected: json.Number("2"), want: true},
		{actual: "prod-east", op: "contains", expected: "prod", want: true},
		{actual: true, op: "==", expected: "true", want: true},
		{actual: "prod", op: "ends_with", expected: "east", want: false},
	} {
		got, err := compare(tc.actual, tc.op.(string), tc.expected)
		if err != nil {
			t.Fatalf("compare(%#v, %q, %#v) error = %v", tc.actual, tc.op, tc.expected, err)
		}
		if got != tc.want {
			t.Errorf("compare(%#v, %q, %#v) = %v, want %v", tc.actual, tc.op, tc.expected, got, tc.want)
		}
	}
}

func TestMetricValidationRequiresAnExactMetricConstraintOnEveryOrBranch(t *testing.T) {
	now := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		query   string
		wantErr string
	}{
		{
			name:  "each branch uses exact equality",
			query: `metrics | where (metric == "cpu" and state == "running") or (metric == "memory" and state == "running") | range last 1h`,
		},
		{
			name:  "each branch uses exact in selector",
			query: `metrics | where metric in ("cpu", "memory") or metric == "disk" | range last 1h`,
		},
		{
			name:    "or branch without metric constraint",
			query:   `metrics | where metric == "cpu" or state == "running" | range last 1h`,
			wantErr: "every OR branch",
		},
		{
			name:    "non exact metric operator",
			query:   `metrics | where metric contains "cpu" or metric == "memory" | range last 1h`,
			wantErr: "every OR branch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.query, now)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Parse() error = %v, want a valid metric query", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestMetricInSelectorsExpandToUniqueMetricNames(t *testing.T) {
	query, err := Parse(`metrics | where metric in ("cpu", "memory", "cpu") | range last 1h`, time.Time{})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	selectors, constrained := MetricSelectors(query.Filter)
	if !constrained {
		t.Fatal("MetricSelectors() reported an unconstrained metric in-list")
	}
	if want := []string{"cpu", "memory"}; !reflect.DeepEqual(selectors, want) {
		t.Fatalf("MetricSelectors() = %#v, want %#v", selectors, want)
	}
}

func TestParseRejectsDuplicateScopeValues(t *testing.T) {
	for _, query := range []string{
		`resources | scope profile in ("aws-prod", "aws-prod")`,
		`resources | scope provider in ("aws", "aws")`,
		`resources | scope account in ("123", "123")`,
	} {
		t.Run(query, func(t *testing.T) {
			if _, err := Parse(query, time.Time{}); err == nil || !strings.Contains(err.Error(), "duplicate scope value") {
				t.Fatalf("Parse(%q) error = %v, want duplicate scope value rejection", query, err)
			}
		})
	}
}

func TestNativeFieldsRequireOptInAndSharedAllowlistAcrossStages(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{name: "fields", query: `resources | include native | fields native.password`, want: "not allow-listed"},
		{name: "where", query: `resources | include native | where native.password == "redacted"`, want: "not allow-listed"},
		{name: "sort", query: `resources | include native | sort native.password asc`, want: "not allow-listed"},
		{name: "summarize", query: `resources | include native | summarize count() by native.password`, want: "not allow-listed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(tc.query, time.Time{}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse(%q) error = %v, want substring %q", tc.query, err, tc.want)
			}
		})
	}
	if _, err := Parse(`resources | include native | fields native.instance_type`, time.Time{}); err != nil {
		t.Fatalf("Parse() rejected the documented AWS native.instance_type field: %v", err)
	}
}
