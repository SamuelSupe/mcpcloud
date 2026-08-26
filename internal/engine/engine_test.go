package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/dsl"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type engineAdapter struct {
	profile     string
	provider    model.Provider
	rows        []map[string]any
	queryErr    error
	nativePage  provider.Page
	nativeErr   error
	operations  []provider.Operation
	queryCalls  int
	nativeCalls int
	lastNative  provider.NativeRequest
	mu          sync.Mutex
}

func (a *engineAdapter) Provider() model.Provider { return a.provider }
func (a *engineAdapter) Profile() string          { return a.profile }
func (a *engineAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{{Provider: a.provider, Source: model.SourceResources, Status: "ready"}}
}
func (a *engineAdapter) Operations() []provider.Operation { return a.operations }
func (a *engineAdapter) Readiness(context.Context) model.ProfileStatus {
	return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: a.queryErr == nil, Status: "ready"}
}
func (a *engineAdapter) Query(ctx context.Context, request provider.QueryRequest) (provider.Page, error) {
	a.mu.Lock()
	a.queryCalls++
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if a.queryErr != nil {
		return provider.Page{}, a.queryErr
	}
	rows := make([]map[string]any, 0, len(a.rows))
	for _, row := range a.rows {
		copyRow := make(map[string]any, len(row))
		for key, value := range row {
			copyRow[key] = value
		}
		rows = append(rows, copyRow)
	}
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}
func (a *engineAdapter) NativeRead(_ context.Context, request provider.NativeRequest) (provider.Page, error) {
	a.mu.Lock()
	a.nativeCalls++
	a.lastNative = request
	a.mu.Unlock()
	return a.nativePage, a.nativeErr
}

func newTestEngine(t *testing.T, adapters ...provider.Adapter) *Engine {
	t.Helper()
	limits := config.Default().Limits
	limits.MaxPageSize = 500
	return newTestEngineWithLimits(t, limits, adapters...)
}

func newTestEngineWithLimits(t *testing.T, limits config.Limits, adapters ...provider.Adapter) *Engine {
	t.Helper()
	registry, err := provider.NewRegistry(adapters...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return New(registry, limits)
}

type concurrencyAdapter struct {
	profile  string
	provider model.Provider
	entered  chan string
	release  chan struct{}

	mu        sync.Mutex
	active    int
	maxActive int
}

func (a *concurrencyAdapter) Provider() model.Provider { return a.provider }
func (a *concurrencyAdapter) Profile() string          { return a.profile }
func (a *concurrencyAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{{Provider: a.provider, Source: model.SourceResources, Status: "available"}}
}
func (a *concurrencyAdapter) Operations() []provider.Operation {
	return []provider.Operation{{Name: "aws.test.read", Provider: a.provider, Service: "test"}}
}
func (a *concurrencyAdapter) Readiness(context.Context) model.ProfileStatus {
	return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: "ready"}
}
func (a *concurrencyAdapter) Query(ctx context.Context, _ provider.QueryRequest) (provider.Page, error) {
	return a.enter(ctx, "query")
}
func (a *concurrencyAdapter) NativeRead(ctx context.Context, _ provider.NativeRequest) (provider.Page, error) {
	return a.enter(ctx, "native")
}
func (a *concurrencyAdapter) enter(ctx context.Context, kind string) (provider.Page, error) {
	a.mu.Lock()
	a.active++
	if a.active > a.maxActive {
		a.maxActive = a.active
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.active--
		a.mu.Unlock()
	}()
	select {
	case a.entered <- kind:
	case <-ctx.Done():
		return provider.Page{}, ctx.Err()
	}
	select {
	case <-a.release:
		return provider.Page{Rows: []map[string]any{{"id": kind}}, Scanned: 1, Requests: 1}, nil
	case <-ctx.Done():
		return provider.Page{}, ctx.Err()
	}
}
func (a *concurrencyAdapter) maximumActive() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxActive
}

type auditAdapter struct {
	profile  string
	provider model.Provider

	mu          sync.Mutex
	queryCalls  int
	nativeCalls int
}

func (a *auditAdapter) Provider() model.Provider { return a.provider }
func (a *auditAdapter) Profile() string          { return a.profile }
func (a *auditAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{{
		Provider:   a.provider,
		Source:     model.SourceResources,
		Status:     "available",
		Operations: []string{"aws.inventory.search"},
	}}
}
func (a *auditAdapter) Operations() []provider.Operation {
	return []provider.Operation{{
		Name:     "aws.inventory.search",
		Provider: a.provider,
		Service:  "inventory",
		Parameters: map[string]any{
			"account_id": map[string]any{"type": "string"},
			"password":   map[string]any{"type": "string"},
		},
	}}
}
func (a *auditAdapter) Readiness(context.Context) model.ProfileStatus {
	return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: "ready"}
}
func (a *auditAdapter) Query(ctx context.Context, _ provider.QueryRequest) (provider.Page, error) {
	a.mu.Lock()
	a.queryCalls++
	call := a.queryCalls
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if call == 1 {
		return provider.Page{}, &provider.Error{Code: "permission_denied", Operation: "https://evil.example/?token=super-secret", Message: "password=super-secret", Retryable: true}
	}
	return provider.Page{Rows: []map[string]any{{"id": "row-secret", "scope": map[string]any{"account_id": "account-1"}}}, Scanned: 1, Requests: 1}, nil
}
func (a *auditAdapter) NativeRead(ctx context.Context, _ provider.NativeRequest) (provider.Page, error) {
	a.mu.Lock()
	a.nativeCalls++
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	return provider.Page{Rows: []map[string]any{{"id": "native-row-secret"}}, Scanned: 1, Requests: 1}, nil
}

type auditRecorder struct {
	mu     sync.Mutex
	events []AuditEvent
}

func (r *auditRecorder) Record(_ context.Context, event AuditEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}
func (r *auditRecorder) snapshot() []AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AuditEvent(nil), r.events...)
}

func TestQueryKeepsSuccessfulRowsWhenOneTargetFails(t *testing.T) {
	good := &engineAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows:     []map[string]any{{"provider": "aws", "profile": "aws-prod", "domain": "compute", "id": "i-1"}},
	}
	failed := &engineAdapter{
		profile:  "gcp-prod",
		provider: model.ProviderGCP,
		queryErr: &provider.Error{Code: "permission_denied", Message: "access token was rejected", Operation: "list_instances"},
	}
	engine := newTestEngine(t, good, failed)
	query, err := dsl.Parse(`resources | scope profile in ("aws-prod", "gcp-prod")`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}

	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Status != "partial" {
		t.Fatalf("Status = %q, want partial", result.Status)
	}
	if !reflect.DeepEqual(result.Coverage, model.Coverage{Planned: 2, Succeeded: 1, Failed: 1}) {
		t.Fatalf("Coverage = %#v, want one successful and one failed target", result.Coverage)
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != "i-1" {
		t.Fatalf("Rows = %#v, want successful target row", result.Rows)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("Errors = %#v, want one provider error", result.Errors)
	}
	if result.Errors[0].Code != "permission_denied" || result.Errors[0].Message != "provider returned a sensitive error; details were redacted" {
		t.Fatalf("sanitized error = %#v, want code and redacted message", result.Errors[0])
	}
}

func TestQueryFiltersSortsAndProjectsRows(t *testing.T) {
	adapter := &engineAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows: []map[string]any{
			{"provider": "aws", "profile": "aws-prod", "domain": "compute", "state": "running", "name": "web-a", "native": map[string]any{"instance_type": "t3.small"}},
			{"provider": "aws", "profile": "aws-prod", "domain": "compute", "state": "stopped", "name": "web-c"},
			{"provider": "aws", "profile": "aws-prod", "domain": "compute", "state": "running", "name": "web-b"},
		},
	}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources | where domain == "compute" and state == "running" | sort name desc | fields name, state`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	wantRows := []map[string]any{{"name": "web-b", "state": "running"}, {"name": "web-a", "state": "running"}}
	if !reflect.DeepEqual(result.Rows, wantRows) {
		t.Fatalf("Rows = %#v, want %#v", result.Rows, wantRows)
	}
	if !reflect.DeepEqual(result.Columns, []string{"name", "state"}) {
		t.Fatalf("Columns = %#v, want projected columns", result.Columns)
	}
	if _, ok := result.Rows[0]["native"]; ok {
		t.Fatal("projected row exposed native data without include native")
	}
}

func TestQueryAggregatesAndSortsGroups(t *testing.T) {
	adapter := &engineAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows: []map[string]any{
			{"provider": "aws", "attributes": map[string]any{"cost": 10.0}},
			{"provider": "aws", "attributes": map[string]any{"cost": 20.0}},
			{"provider": "gcp", "attributes": map[string]any{"cost": 5.0}},
		},
	}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources | summarize count(), sum(attributes.cost), avg(attributes.cost) by provider | sort provider asc`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("Rows = %#v, want two provider groups", result.Rows)
	}
	if result.Rows[0]["provider"] != "aws" || result.Rows[0]["count"] != 2 || result.Rows[0]["sum_attributes_cost"] != 30.0 || result.Rows[0]["avg_attributes_cost"] != 15.0 {
		t.Fatalf("aws aggregate row = %#v, want count=2 sum=30 avg=15", result.Rows[0])
	}
	if result.Rows[1]["provider"] != "gcp" || result.Rows[1]["count"] != 1 || result.Rows[1]["sum_attributes_cost"] != 5.0 || result.Rows[1]["avg_attributes_cost"] != 5.0 {
		t.Fatalf("gcp aggregate row = %#v, want count=1 sum=5 avg=5", result.Rows[1])
	}
}

func TestQueryPaginatesAndCursorIsSingleUse(t *testing.T) {
	adapter := &engineAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows: []map[string]any{
			{"name": "a", "id": "1"},
			{"name": "b", "id": "2"},
			{"name": "c", "id": "3"},
		},
	}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources | sort name asc`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	first, err := engine.Query(context.Background(), query, 2)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(first.Rows) != 2 || first.Rows[0]["name"] != "a" || first.Rows[1]["name"] != "b" {
		t.Fatalf("first page = %#v, want a,b", first.Rows)
	}
	if first.NextCursor == "" {
		t.Fatal("first page has no next cursor")
	}
	second, err := engine.Next(first.NextCursor)
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if len(second.Rows) != 1 || second.Rows[0]["name"] != "c" || second.NextCursor != "" {
		t.Fatalf("second page = %#v (cursor %q), want final row and no cursor", second.Rows, second.NextCursor)
	}
	if _, err := engine.Next(first.NextCursor); err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatalf("reusing cursor error = %v, want invalid/expired rejection", err)
	}
}

func TestQueryCursorExpiresWithoutSleeping(t *testing.T) {
	adapter := &engineAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		rows:     []map[string]any{{"id": "1"}, {"id": "2"}},
	}
	engine := newTestEngine(t, adapter)
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clock := base
	engine.cursors.clock = func() time.Time { return clock }
	query, err := dsl.Parse(`resources`, base)
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 1)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.NextCursor == "" {
		t.Fatal("Query() did not create a cursor")
	}
	clock = base.Add(config.Default().Limits.CursorTTL + time.Nanosecond)
	if _, err := engine.Next(result.NextCursor); err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatalf("expired cursor error = %v, want invalid/expired rejection", err)
	}
}

func TestNativeReadOnlyAllowlistRejectsUnregisteredOperations(t *testing.T) {
	adapter := &engineAdapter{
		profile:    "aws-prod",
		provider:   model.ProviderAWS,
		operations: []provider.Operation{{Name: "aws.ec2.describe_instances", Provider: model.ProviderAWS, Service: "ec2"}},
		nativePage: provider.Page{Rows: []map[string]any{{"id": "i-1"}}, Requests: 1, Scanned: 1},
	}
	engine := newTestEngine(t, adapter)

	page, err := engine.NativeRead(context.Background(), "aws-prod", provider.NativeRequest{Operation: "aws.ec2.describe_instances", Limit: 1})
	if err != nil {
		t.Fatalf("registered NativeRead() error = %v", err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("registered NativeRead() rows = %#v, want one row", page.Rows)
	}
	if adapter.nativeCalls != 1 || adapter.lastNative.Operation != "aws.ec2.describe_instances" {
		t.Fatalf("adapter native calls = %d request=%#v, want one registered operation", adapter.nativeCalls, adapter.lastNative)
	}

	for _, operation := range []string{"aws.ec2.delete_instance", "https://169.254.169.254/latest/meta-data", "ListInstances"} {
		_, err := engine.NativeRead(context.Background(), "aws-prod", provider.NativeRequest{Operation: operation, Limit: 1})
		if err == nil || !strings.Contains(err.Error(), "not registered") {
			t.Errorf("NativeRead(%q) error = %v, want unregistered-operation rejection", operation, err)
		}
	}
	if adapter.nativeCalls != 1 {
		t.Fatalf("adapter native calls = %d after rejected operations, want no additional provider calls", adapter.nativeCalls)
	}
}

func TestNativeReadRejectsUnknownProfileAndOversizedPage(t *testing.T) {
	adapter := &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS, operations: []provider.Operation{{Name: "aws.ec2.describe_instances"}}}
	engine := newTestEngine(t, adapter)
	if _, err := engine.NativeRead(context.Background(), "missing", provider.NativeRequest{Operation: "aws.ec2.describe_instances"}); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("unknown profile error = %v, want unknown profile", err)
	}
	if _, err := engine.NativeRead(context.Background(), "aws-prod", provider.NativeRequest{Operation: "aws.ec2.describe_instances", Limit: config.Default().Limits.MaxPageSize + 1}); err == nil || !strings.Contains(err.Error(), "page size exceeds") {
		t.Fatalf("oversized native page error = %v, want page-size rejection", err)
	}
}

func TestEngineConcurrencyLimitIsSharedByQueryAndNativeRead(t *testing.T) {
	adapter := &concurrencyAdapter{
		profile:  "aws-prod",
		provider: model.ProviderAWS,
		entered:  make(chan string, 2),
		release:  make(chan struct{}),
	}
	limits := config.Default().Limits
	limits.Concurrency = 1
	engine := newTestEngineWithLimits(t, limits, adapter)
	query, err := dsl.Parse(`resources | scope profile in ("aws-prod")`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}

	queryDone := make(chan error, 1)
	go func() {
		_, queryErr := engine.Query(context.Background(), query, 100)
		queryDone <- queryErr
	}()
	if kind := <-adapter.entered; kind != "query" {
		t.Fatalf("first provider call = %q, want query", kind)
	}

	nativeContext, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	nativeDone := make(chan error, 1)
	go func() {
		_, nativeErr := engine.NativeRead(nativeContext, adapter.profile, provider.NativeRequest{Operation: "aws.test.read", Limit: 1})
		nativeDone <- nativeErr
	}()

	// Keep the first call in-flight until the second context has had a chance
	// to time out. A separate per-request semaphore would let NativeRead enter
	// the adapter here and raise maxActive to two.
	time.Sleep(150 * time.Millisecond)
	close(adapter.release)
	if queryErr := <-queryDone; queryErr != nil {
		t.Fatalf("Query() error = %v", queryErr)
	}
	if nativeErr := <-nativeDone; !errors.Is(nativeErr, context.DeadlineExceeded) {
		t.Fatalf("NativeRead() error = %v, want shared-slot deadline", nativeErr)
	}
	if got := adapter.maximumActive(); got != 1 {
		t.Fatalf("maximum simultaneous provider calls = %d, want shared concurrency limit 1", got)
	}
}

func TestExplainAndQueryRejectTargetBudgetsBeforeProviderCalls(t *testing.T) {
	for _, tc := range []struct {
		name                string
		maxProviderRequests int
		maxScannedItems     int
		wantError           string
	}{
		{name: "provider request budget", maxProviderRequests: 1, maxScannedItems: 50_000, wantError: "provider request budget"},
		{name: "scanned item budget", maxProviderRequests: 500, maxScannedItems: 1, wantError: "scanned item budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS}
			second := &engineAdapter{profile: "gcp-prod", provider: model.ProviderGCP}
			limits := config.Default().Limits
			limits.MaxProviderRequests = tc.maxProviderRequests
			limits.MaxScannedItems = tc.maxScannedItems
			engine := newTestEngineWithLimits(t, limits, first, second)
			query, err := dsl.Parse(`resources | scope profile in ("aws-prod", "gcp-prod")`, time.Now().UTC())
			if err != nil {
				t.Fatalf("dsl.Parse() error = %v", err)
			}

			if _, err := engine.Explain(query); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Explain() error = %v, want %q rejection", err, tc.wantError)
			}
			if first.queryCalls != 0 || second.queryCalls != 0 {
				t.Fatalf("Explain() provider calls = (%d, %d), want no calls", first.queryCalls, second.queryCalls)
			}
			if _, err := engine.Query(context.Background(), query, 100); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Query() error = %v, want %q rejection", err, tc.wantError)
			}
			if first.queryCalls != 0 || second.queryCalls != 0 {
				t.Fatalf("Query() provider calls = (%d, %d), want no calls", first.queryCalls, second.queryCalls)
			}
		})
	}
}

func TestQueryHardTruncatesSinglePageAtPerTargetScanBudget(t *testing.T) {
	adapter := &queryRequestAdapter{
		profile: "aws-prod", provider: model.ProviderAWS,
		queryPage: func(provider.QueryRequest) provider.Page {
			rows := make([]map[string]any, 5)
			for i := range rows {
				rows[i] = map[string]any{"id": fmt.Sprintf("row-%d", i)}
			}
			return provider.Page{Rows: rows, Scanned: 5, Requests: 1, NextToken: "provider-next"}
		},
	}
	limits := config.Default().Limits
	limits.MaxScannedItems = 2
	engine := newTestEngineWithLimits(t, limits, adapter)
	query, err := dsl.Parse(`resources | scope profile in ("aws-prod")`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Rows) != 2 {
		t.Fatalf("Rows = %#v, want two rows within per-target scan budget", result.Rows)
	}
	if result.Stats.Scanned != 2 || !result.Stats.Truncated {
		t.Fatalf("Stats = %#v, want Scanned=2 and Truncated=true", result.Stats)
	}
}

func TestCursorStorePrunesExpiredEntriesAndBoundsCapacity(t *testing.T) {
	store := NewCursorStore(time.Minute, 1)
	if store.maxRows != 50_000 {
		t.Fatalf("CursorStore maxRows = %d, want 50000", store.maxRows)
	}
	clock := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	store.clock = func() time.Time { return clock }
	first := store.Put(cursorState{rows: []map[string]any{{"id": "first"}}, pageSize: 1})
	second := store.Put(cursorState{rows: []map[string]any{{"id": "second"}}, pageSize: 1})
	if _, ok := store.Next(first); ok {
		t.Fatal("capacity-bounded cursor store retained the oldest cursor")
	}
	if _, ok := store.Next(second); !ok {
		t.Fatal("capacity-bounded cursor store removed the newest cursor")
	}
	third := store.Put(cursorState{rows: []map[string]any{{"id": "third"}}, pageSize: 1})
	clock = clock.Add(time.Minute)
	if _, ok := store.Next(third); ok {
		t.Fatal("cursor store returned an expired cursor")
	}

	rowBounded := NewCursorStore(time.Minute, 128)
	rowBounded.maxRows = 2
	rowBounded.clock = func() time.Time { return clock }
	rowLimited := rowBounded.Put(cursorState{rows: []map[string]any{{"id": "row-1"}, {"id": "row-2"}}, pageSize: 1})
	newest := rowBounded.Put(cursorState{rows: []map[string]any{{"id": "new"}}, pageSize: 1})
	if _, ok := rowBounded.Next(rowLimited); ok {
		t.Fatal("row-bounded cursor store retained an older cursor after exceeding maxRows")
	}
	if _, ok := rowBounded.Next(newest); !ok {
		t.Fatal("row-bounded cursor store removed the newest cursor")
	}
}

func TestQueryContextCancellationIsReportedPerTarget(t *testing.T) {
	adapter := &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS, rows: []map[string]any{{"id": "1"}}}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := engine.Query(ctx, query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v, want result with per-target cancellation", err)
	}
	if result.Status != "failed" || result.Coverage.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("cancelled query result = %#v, want one failed target", result)
	}
	queryError := result.Errors[0]
	if queryError.Code != "canceled" || queryError.Message != "provider request canceled" || queryError.Retryable {
		t.Fatalf("cancelled query error = %#v, want canceled/non-retryable error", queryError)
	}
}

func TestQueryDeadlineIsReportedAsRetryableTimeout(t *testing.T) {
	adapter := &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS, rows: []map[string]any{{"id": "1"}}}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	result, err := engine.Query(ctx, query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v, want result with per-target timeout", err)
	}
	if result.Status != "failed" || result.Coverage.Failed != 1 || len(result.Errors) != 1 {
		t.Fatalf("deadline query result = %#v, want one failed target", result)
	}
	queryError := result.Errors[0]
	if queryError.Code != "timeout" || queryError.Message != "provider request deadline exceeded" || !queryError.Retryable {
		t.Fatalf("deadline query error = %#v, want retryable timeout", queryError)
	}
}

func TestAuditEventsCorrelateQueryRetriesAndNativeReadWithoutSensitivePayload(t *testing.T) {
	adapter := &auditAdapter{profile: "aws-prod", provider: model.ProviderAWS}
	engine := newTestEngine(t, adapter)
	recorder := &auditRecorder{}
	engine.SetAuditSink(recorder)
	query, err := dsl.Parse(`resources | scope profile in ("aws-prod")`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil || result.Status != "ok" || result.QueryID == "" {
		t.Fatalf("Query() = (%#v, %v), want successful query id after retry", result, err)
	}
	events := recorder.snapshot()
	if len(events) != 2 {
		t.Fatalf("query audit events = %#v, want one event per retry attempt", events)
	}
	if events[0].CorrelationID != result.QueryID || events[1].CorrelationID != result.QueryID {
		t.Fatalf("query audit correlations = (%q, %q), want QueryID %q", events[0].CorrelationID, events[1].CorrelationID, result.QueryID)
	}
	if events[0].Attempt != 1 || events[0].Status != "failed" || events[0].ErrorCode != "permission_denied" || !events[0].Retryable {
		t.Fatalf("first query audit event = %#v, want retryable safe failure", events[0])
	}
	if events[1].Attempt != 2 || events[1].Status != "ok" || events[1].Rows != 1 || events[1].Scanned != 1 {
		t.Fatalf("second query audit event = %#v, want successful row counts", events[1])
	}
	for _, event := range events {
		if event.Provider != model.ProviderAWS || event.Profile != "aws-prod" || event.Source != model.SourceResources || event.Operation != "aws.inventory.search" {
			t.Fatalf("query audit attribution = %#v, want AWS resources operation", event)
		}
	}

	_, err = engine.NativeReadWithID(context.Background(), "native-correlation", "aws-prod", provider.NativeRequest{
		Operation: "aws.inventory.search",
		Params:    map[string]any{"account_id": "account-1", "password": "super-secret"},
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("NativeReadWithID() error = %v", err)
	}
	events = recorder.snapshot()
	if len(events) != 3 {
		t.Fatalf("query/native audit events = %#v, want one native event after two query events", events)
	}
	nativeEvent := events[2]
	if nativeEvent.CorrelationID != "native-correlation" || nativeEvent.Source != model.SourceResources || nativeEvent.Scope != "account-1" || nativeEvent.Status != "ok" || nativeEvent.Rows != 1 || nativeEvent.Scanned != 1 {
		t.Fatalf("native audit event = %#v, want correlated safe scope/row metadata", nativeEvent)
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("Marshal(audit events) error = %v", err)
	}
	auditText := string(encoded)
	for _, forbidden := range []string{"super-secret", "row-secret", "native-row-secret", "password", "account_id", "evil.example"} {
		if strings.Contains(auditText, forbidden) {
			t.Fatalf("typed audit events contain forbidden provider/request data %q: %s", forbidden, auditText)
		}
	}
}

func TestToQueryErrorRedactsSensitiveMessagesButPreservesShortSafeErrors(t *testing.T) {
	sensitive := toQueryError(Target{Profile: "aws-prod"}, errors.New("request contained password=secret"))
	if sensitive.Message != "provider returned a sensitive error; details were redacted" {
		t.Fatalf("sensitive message = %q, want redaction", sensitive.Message)
	}
	safe := toQueryError(Target{Profile: "aws-prod"}, errors.New("temporary unavailable"))
	if safe.Message != "temporary unavailable" {
		t.Fatalf("safe message = %q, want original short error", safe.Message)
	}
}

func TestEngineExplainReportsTargetsAndResidualFilter(t *testing.T) {
	engine := newTestEngine(t, &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS})
	query, err := dsl.Parse(`resources | scope profile in ("aws-prod") | where state == "running" | fields id, name`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	explained, err := engine.Explain(query)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if !reflect.DeepEqual(explained.Profiles, []string{"aws-prod"}) || len(explained.Targets) != 1 {
		t.Fatalf("Explain() targets = %#v profiles = %#v, want aws-prod target", explained.Targets, explained.Profiles)
	}
	if explained.Filter == "" || len(explained.Residual) != 1 || explained.Fields[0] != "id" {
		t.Fatalf("Explain() = %#v, want filter residual and fields", explained)
	}
}

func TestQueryRowsRemainDeterministicallySortedForEqualFields(t *testing.T) {
	adapter := &engineAdapter{profile: "aws-prod", provider: model.ProviderAWS, rows: []map[string]any{{"name": "same", "id": "2"}, {"name": "same", "id": "1"}}}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources | sort name asc`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Rows) != 2 || result.Rows[0]["id"] != "2" || result.Rows[1]["id"] != "1" {
		t.Fatalf("Rows = %#v, want equal-key rows to retain provider order", result.Rows)
	}
}

type queryRequestAdapter struct {
	profile       string
	provider      model.Provider
	regions       []string
	scopes        []string
	queryPage     func(provider.QueryRequest) provider.Page
	targetRegions func(model.Source, []string) ([]string, error)
	mu            sync.Mutex
	queryCalls    []provider.QueryRequest
}

func (a *queryRequestAdapter) Provider() model.Provider { return a.provider }
func (a *queryRequestAdapter) Profile() string          { return a.profile }
func (a *queryRequestAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{
		{Provider: a.provider, Source: model.SourceResources, Status: "available"},
		{Provider: a.provider, Source: model.SourceMetrics, Status: "available"},
		{Provider: a.provider, Source: model.SourceCosts, Status: "available"},
	}
}
func (a *queryRequestAdapter) Operations() []provider.Operation { return nil }
func (a *queryRequestAdapter) Readiness(context.Context) model.ProfileStatus {
	return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: "ready"}
}
func (a *queryRequestAdapter) Query(ctx context.Context, request provider.QueryRequest) (provider.Page, error) {
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	request.Metrics = append([]string(nil), request.Metrics...)
	request.Accounts = append([]string(nil), request.Accounts...)
	a.mu.Lock()
	a.queryCalls = append(a.queryCalls, request)
	a.mu.Unlock()
	if a.queryPage != nil {
		return a.queryPage(request), nil
	}
	return provider.Page{Rows: []map[string]any{{"profile": a.profile, "region": request.Region}}, Scanned: 1, Requests: 1}, nil
}
func (a *queryRequestAdapter) NativeRead(context.Context, provider.NativeRequest) (provider.Page, error) {
	return provider.Page{}, nil
}
func (a *queryRequestAdapter) ConfiguredRegions() []string {
	return append([]string(nil), a.regions...)
}

func (a *queryRequestAdapter) TargetRegions(source model.Source, requested []string) ([]string, error) {
	if a.targetRegions != nil {
		return a.targetRegions(source, requested)
	}
	if source == model.SourceCosts {
		return []string{""}, nil
	}
	regions := append([]string(nil), requested...)
	if len(regions) == 0 {
		regions = append(regions, a.regions...)
	}
	if len(regions) == 0 {
		regions = []string{""}
	}
	return regions, nil
}

func (a *queryRequestAdapter) ConfiguredScopesFor(source model.Source) []string {
	if source == model.SourceCosts {
		return nil
	}
	return append([]string(nil), a.scopes...)
}
func (a *queryRequestAdapter) requests() []provider.QueryRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]provider.QueryRequest, len(a.queryCalls))
	copy(result, a.queryCalls)
	return result
}

func TestQueryDispatchesOneMetricRequestPerSelectorAcrossTargetsAndPreservesFilter(t *testing.T) {
	metricPage := func(profile string) func(provider.QueryRequest) provider.Page {
		return func(request provider.QueryRequest) provider.Page {
			metric := ""
			if len(request.Metrics) == 1 {
				metric = request.Metrics[0]
			}
			account := ""
			if len(request.Accounts) == 1 {
				account = request.Accounts[0]
			}
			id := profile + "/" + account + "/" + metric
			return provider.Page{
				Rows: []map[string]any{
					{"profile": profile, "account": account, "metric": metric, "state": "running", "id": id + "/running"},
					{"profile": profile, "account": account, "metric": metric, "state": "stopped", "id": id + "/stopped"},
				},
				Scanned:  2,
				Requests: 1,
			}
		}
	}
	prod := &queryRequestAdapter{profile: "metrics-prod", provider: model.ProviderAWS, queryPage: metricPage("metrics-prod")}
	stage := &queryRequestAdapter{profile: "metrics-stage", provider: model.ProviderGCP, queryPage: metricPage("metrics-stage")}
	engine := newTestEngine(t, prod, stage)
	query, err := dsl.Parse(`metrics | scope profile in ("metrics-prod", "metrics-stage"), account in ("account-a", "account-b") | where metric in ("cpu", "memory") and state == "running" | range last 1h`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}

	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if result.Status != "ok" || !reflect.DeepEqual(result.Coverage, model.Coverage{Planned: 4, Succeeded: 4}) {
		t.Fatalf("query status/coverage = (%q, %#v), want one successful result per profile/account target", result.Status, result.Coverage)
	}
	if result.Stats.Requests != 8 {
		t.Fatalf("provider request count = %d, want one request per selector per target", result.Stats.Requests)
	}

	for _, adapter := range []*queryRequestAdapter{prod, stage} {
		requests := adapter.requests()
		if len(requests) != 4 {
			t.Fatalf("%s recorded provider requests = %#v, want one per selector per account", adapter.profile, requests)
		}
		counts := map[string]int{}
		for _, request := range requests {
			if request.Source != model.SourceMetrics {
				t.Errorf("%s request source = %q, want metrics", adapter.profile, request.Source)
			}
			if len(request.Metrics) != 1 {
				t.Errorf("%s request metrics = %#v, want exactly one selector", adapter.profile, request.Metrics)
				continue
			}
			if len(request.Accounts) != 1 {
				t.Errorf("%s request accounts = %#v, want exactly one account scope", adapter.profile, request.Accounts)
				continue
			}
			counts[request.Accounts[0]+"/"+request.Metrics[0]]++
		}
		want := map[string]int{"account-a/cpu": 1, "account-a/memory": 1, "account-b/cpu": 1, "account-b/memory": 1}
		if !reflect.DeepEqual(counts, want) {
			t.Fatalf("%s metric requests by account = %#v, want %#v", adapter.profile, counts, want)
		}
	}

	if len(result.Rows) != 8 {
		t.Fatalf("filtered rows = %#v, want one running row per selector and target", result.Rows)
	}
	seenIDs := map[string]bool{}
	for _, row := range result.Rows {
		profile, _ := row["profile"].(string)
		account, _ := row["account"].(string)
		metric, _ := row["metric"].(string)
		id, _ := row["id"].(string)
		wantID := profile + "/" + account + "/" + metric + "/running"
		if (profile != "metrics-prod" && profile != "metrics-stage") || (account != "account-a" && account != "account-b") || (metric != "cpu" && metric != "memory") || row["state"] != "running" || id != wantID {
			t.Errorf("filtered row = %#v, want a running row for a requested profile/account/metric", row)
			continue
		}
		if seenIDs[id] {
			t.Errorf("duplicate filtered row id %q", id)
		}
		seenIDs[id] = true
	}
	if len(seenIDs) != 8 {
		t.Fatalf("filtered row ids = %#v, want eight unique running rows", seenIDs)
	}
}

func TestCostsUseOneGlobalTargetPerProfileWithoutConfiguredScopeFanout(t *testing.T) {
	aws := &queryRequestAdapter{
		profile:  "aws-billing",
		provider: model.ProviderAWS,
		regions:  []string{"us-east-1", "eu-west-1"},
		scopes:   []string{"account-a", "account-b"},
	}
	gcp := &queryRequestAdapter{
		profile:  "gcp-billing",
		provider: model.ProviderGCP,
		regions:  []string{"us-central1", "asia-east1"},
		scopes:   []string{"project-a", "project-b"},
	}
	engine := newTestEngine(t, aws, gcp)
	query, err := dsl.Parse(`costs | range last 24h`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}

	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if !reflect.DeepEqual(result.Coverage, model.Coverage{Planned: 2, Succeeded: 2}) {
		t.Fatalf("cost query coverage = %#v, want one successful target per profile", result.Coverage)
	}
	if result.Stats.Requests != 2 || len(result.Rows) != 2 {
		t.Fatalf("cost query requests/rows = (%d, %#v), want two total", result.Stats.Requests, result.Rows)
	}

	for _, adapter := range []*queryRequestAdapter{aws, gcp} {
		requests := adapter.requests()
		if len(requests) != 1 {
			t.Fatalf("%s requests = %#v, want one global target", adapter.profile, requests)
		}
		request := requests[0]
		if request.Source != model.SourceCosts || request.Region != "" || len(request.Accounts) != 0 {
			t.Fatalf("%s cost request = %#v, want costs with global region and no configured account", adapter.profile, request)
		}
	}
}

func TestGlobalCostRegionScopeIsFilteredLocallyAndExplainedAsResidual(t *testing.T) {
	adapter := &queryRequestAdapter{
		profile:  "aws-billing",
		provider: model.ProviderAWS,
		queryPage: func(provider.QueryRequest) provider.Page {
			return provider.Page{
				Rows: []map[string]any{
					{"profile": "aws-billing", "region": "us-east-1", "id": "matching"},
					{"profile": "aws-billing", "region": "eu-west-1", "id": "filtered"},
				},
				Scanned:  2,
				Requests: 1,
			}
		},
		targetRegions: func(source model.Source, requested []string) ([]string, error) {
			if source != model.SourceCosts {
				return requested, nil
			}
			return []string{""}, nil
		},
	}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`costs | scope profile in ("aws-billing"), region in ("us-east-1") | range last 24h`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	plan, err := engine.Explain(query)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].Region != "" {
		t.Fatalf("Explain() targets = %#v, want one global target", plan.Targets)
	}
	if !containsString(plan.Residual, "scope.region") || containsString(plan.Pushdown, "scope.region") {
		t.Fatalf("Explain() pushdown/residual = (%#v, %#v), want local region residual", plan.Pushdown, plan.Residual)
	}
	result, err := engine.Query(context.Background(), query, 100)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(result.Rows) != 1 || result.Rows[0]["id"] != "matching" {
		t.Fatalf("Query() rows = %#v, want only matching region", result.Rows)
	}
}

func TestRegionSpecificTargetsPushDownRegionScope(t *testing.T) {
	adapter := &queryRequestAdapter{
		profile:  "resource-prod",
		provider: model.ProviderAWS,
		regions:  []string{"us-east-1", "eu-west-1"},
	}
	engine := newTestEngine(t, adapter)
	query, err := dsl.Parse(`resources | scope profile in ("resource-prod"), region in ("us-east-1")`, time.Now().UTC())
	if err != nil {
		t.Fatalf("dsl.Parse() error = %v", err)
	}
	plan, err := engine.Explain(query)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if len(plan.Targets) != 1 || plan.Targets[0].Region != "us-east-1" {
		t.Fatalf("Explain() targets = %#v, want one exact-region target", plan.Targets)
	}
	if !containsString(plan.Pushdown, "scope.region") || containsString(plan.Residual, "scope.region") {
		t.Fatalf("Explain() pushdown/residual = (%#v, %#v), want region pushdown", plan.Pushdown, plan.Residual)
	}
}
