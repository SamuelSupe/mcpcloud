package probe

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type probeTestAdapter struct {
	profile    string
	provider   model.Provider
	capability provider.Capability
	operation  provider.Operation
	status     model.ProfileStatus

	mu          sync.Mutex
	queryCalls  int
	nativeCalls int
}

func (a *probeTestAdapter) Provider() model.Provider { return a.provider }
func (a *probeTestAdapter) Profile() string          { return a.profile }
func (a *probeTestAdapter) Capabilities() []provider.Capability {
	return []provider.Capability{a.capability}
}
func (a *probeTestAdapter) Operations() []provider.Operation {
	return []provider.Operation{a.operation}
}
func (a *probeTestAdapter) Readiness(context.Context) model.ProfileStatus {
	if a.status.Name == "" {
		return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: StatusReady, Scopes: map[string]any{"accounts": []string{"account-1"}}}
	}
	return a.status
}
func (a *probeTestAdapter) ConfiguredScopesFor(source model.Source) []string {
	if source != model.SourceResources {
		return nil
	}
	var scopes []string
	if a.provider == model.ProviderHuawei {
		if values, ok := a.status.Scopes["projects"].([]string); ok && len(values) > 0 {
			return append([]string(nil), values...)
		}
	}
	if values, ok := a.status.Scopes["accounts"].([]string); ok {
		scopes = append(scopes, values...)
	}
	if a.provider == model.ProviderGCP {
		if values, ok := a.status.Scopes["projects"].([]string); ok {
			for _, value := range values {
				scopes = append(scopes, "projects/"+value)
			}
		}
	}
	return scopes
}
func (a *probeTestAdapter) Query(context.Context, provider.QueryRequest) (provider.Page, error) {
	a.mu.Lock()
	a.queryCalls++
	a.mu.Unlock()
	return provider.Page{}, nil
}
func (a *probeTestAdapter) NativeRead(context.Context, provider.NativeRequest) (provider.Page, error) {
	a.mu.Lock()
	a.nativeCalls++
	a.mu.Unlock()
	return provider.Page{}, nil
}
func (a *probeTestAdapter) calls() (int, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.queryCalls, a.nativeCalls
}

type probeNativeCall struct {
	correlationID string
	profile       string
	request       provider.NativeRequest
}

type probeTestReader struct {
	mu        sync.Mutex
	calls     []probeNativeCall
	responses map[string]struct {
		page provider.Page
		err  error
	}
	sequences map[string][]struct {
		page provider.Page
		err  error
	}
}

func (r *probeTestReader) NativeReadWithID(ctx context.Context, correlationID, profile string, request provider.NativeRequest) (provider.Page, error) {
	r.mu.Lock()
	r.calls = append(r.calls, probeNativeCall{correlationID: correlationID, profile: profile, request: request})
	response := r.responses[profile]
	if sequence := r.sequences[profile]; len(sequence) > 0 {
		response = sequence[0]
		r.sequences[profile] = sequence[1:]
	}
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	return response.page, response.err
}
func (r *probeTestReader) snapshotCalls() []probeNativeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]probeNativeCall(nil), r.calls...)
}

func newProbeTestAdapter(profile string, cloud model.Provider, scopeKey, scopeValue string) *probeTestAdapter {
	operationName := string(cloud) + ".inventory.list_resources"
	return &probeTestAdapter{
		profile:  profile,
		provider: cloud,
		capability: provider.Capability{
			Provider: cloud, Source: model.SourceResources, Status: "inventory",
			Operations: []string{operationName},
		},
		operation: provider.Operation{Name: operationName, Provider: cloud, Service: "inventory", Parameters: map[string]any{}},
		status: model.ProfileStatus{
			Name: profile, Provider: cloud, Ready: true, Status: StatusReady,
			Scopes: map[string]any{scopeKey + "s": []string{scopeValue}},
		},
	}
}

func newProbeTestRunner(t *testing.T, adapters ...provider.Adapter) (*Runner, *probeTestReader) {
	t.Helper()
	registry, err := provider.NewRegistry(adapters...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	reader := &probeTestReader{responses: map[string]struct {
		page provider.Page
		err  error
	}{}, sequences: map[string][]struct {
		page provider.Page
		err  error
	}{}}
	return NewRunner(registry, reader), reader
}

func TestRunnerUsesFixedInventoryNativeReadAndSafeSortedProfileSelection(t *testing.T) {
	aws := newProbeTestAdapter("aws-prod", model.ProviderAWS, "account", "account-1")
	gcp := newProbeTestAdapter("gcp-prod", model.ProviderGCP, "project", "project-1")
	runner, reader := newProbeTestRunner(t, gcp, aws)
	reader.responses["aws-prod"] = struct {
		page provider.Page
		err  error
	}{page: provider.Page{Rows: []map[string]any{{
		"id": "sensitive-row-id", "scope": map[string]any{"account_id": "account-1"},
		"native": map[string]any{"password": "super-secret"},
	}}, Scanned: 1, Requests: 1}}
	reader.responses["gcp-prod"] = struct {
		page provider.Page
		err  error
	}{page: provider.Page{Rows: []map[string]any{{"scope": map[string]any{"project_id": "project-1"}}}, Scanned: 1, Requests: 1}}

	if got, want := runner.Profiles(), []string{"aws-prod", "gcp-prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Profiles() = %#v, want sorted profiles %#v", got, want)
	}
	report, err := runner.Run(context.Background(), []string{"gcp-prod", "aws-prod"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Status != StatusReady || len(report.Profiles) != 2 {
		t.Fatalf("report = %#v, want ready report for two profiles", report)
	}
	if got := []string{report.Profiles[0].Profile, report.Profiles[1].Profile}; !reflect.DeepEqual(got, []string{"aws-prod", "gcp-prod"}) {
		t.Fatalf("report profile order = %#v, want sorted order", got)
	}
	for _, result := range report.Profiles {
		if result.Status != StatusReady || !result.Authenticated || !result.ScopeVerified || result.Operation == "" {
			t.Fatalf("profile probe result = %#v, want authenticated scope-verified fixed operation", result)
		}
	}
	calls := reader.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("NativeReadWithID calls = %#v, want one fixed inventory call per profile", calls)
	}
	for _, call := range calls {
		if call.correlationID == "" || call.request.Operation == "" || call.request.Limit != probePageSize || call.request.Region != "" || len(call.request.Params) != 0 || call.request.PageToken != "" {
			t.Fatalf("probe call = %#v, want fixed operation/limit without arbitrary params", call)
		}
	}
	for _, adapter := range []*probeTestAdapter{aws, gcp} {
		queryCalls, nativeCalls := adapter.calls()
		if queryCalls != 0 || nativeCalls != 0 {
			t.Fatalf("adapter %s calls = (Query %d, NativeRead %d), want probe seam only", adapter.profile, queryCalls, nativeCalls)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal(report) error = %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"sensitive-row-id", "super-secret", "password", "NativeReadWithID"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("probe report contains forbidden provider detail %q: %s", forbidden, text)
		}
	}
}

func TestRunnerContinuesInventoryCursorWhenFirstPageHasNoMatchingScope(t *testing.T) {
	adapter := newProbeTestAdapter("aws-prod", model.ProviderAWS, "account", "account-1")
	runner, reader := newProbeTestRunner(t, adapter)
	reader.sequences["aws-prod"] = []struct {
		page provider.Page
		err  error
	}{
		{page: provider.Page{NextToken: "next-page", Scanned: 1}},
		{page: provider.Page{Rows: []map[string]any{{"scope": map[string]any{"account_id": "account-1"}}}, Scanned: 1}},
	}

	report, err := runner.Run(context.Background(), []string{"aws-prod"})
	if err != nil || report.Status != StatusReady {
		t.Fatalf("report = (%#v, %v), want ready after cursor continuation", report, err)
	}
	if len(reader.snapshotCalls()) != 2 {
		t.Fatalf("NativeReadWithID calls = %#v, want two paginated calls", reader.snapshotCalls())
	}
	calls := reader.snapshotCalls()
	if calls[0].request.Limit != probePageSize || calls[1].request.Limit != probePageSize || calls[1].request.PageToken != "next-page" {
		t.Fatalf("paginated probe calls = %#v, want bounded page size and returned cursor", calls)
	}
}

func TestRunnerRejectsUnknownAndDuplicateProfilesWithoutNativeCalls(t *testing.T) {
	adapter := newProbeTestAdapter("aws-prod", model.ProviderAWS, "account", "account-1")
	runner, reader := newProbeTestRunner(t, adapter)
	for _, profiles := range [][]string{{"missing"}, {"aws-prod", "aws-prod"}} {
		if _, err := runner.Run(context.Background(), profiles); err == nil {
			t.Fatalf("Run(%#v) succeeded, want selection error", profiles)
		}
	}
	if calls := reader.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("NativeReadWithID calls after invalid selections = %#v, want zero", calls)
	}
}

func TestRunnerReportsPartialAllFailuresAndCancellationSafely(t *testing.T) {
	aws := newProbeTestAdapter("aws-prod", model.ProviderAWS, "account", "account-1")
	gcp := newProbeTestAdapter("gcp-prod", model.ProviderGCP, "project", "project-1")
	runner, reader := newProbeTestRunner(t, aws, gcp)
	reader.responses["aws-prod"] = struct {
		page provider.Page
		err  error
	}{page: provider.Page{Rows: []map[string]any{{"scope": map[string]any{"account_id": "account-1"}}}, Scanned: 1, Requests: 1}}
	reader.responses["gcp-prod"] = struct {
		page provider.Page
		err  error
	}{err: &provider.Error{Code: "permission_denied", Operation: "https://evil.example/?token=super-secret", Message: "credential=super-secret", Retryable: true}}

	partial, err := runner.Run(context.Background(), []string{"aws-prod", "gcp-prod"})
	if err != nil || partial.Status != StatusDegraded {
		t.Fatalf("partial report = (%#v, %v), want degraded report", partial, err)
	}
	if partial.Profiles[0].Status != StatusReady || partial.Profiles[1].Status != StatusNotReady || partial.Profiles[1].Error == nil || partial.Profiles[1].Error.Code != "permission_denied" || partial.Profiles[1].Error.Operation != "gcp.inventory.list_resources" {
		t.Fatalf("partial profile results = %#v, want ready plus safe failed result", partial.Profiles)
	}
	partialJSON, err := json.Marshal(partial)
	if err != nil {
		t.Fatalf("Marshal(partial) error = %v", err)
	}
	if strings.Contains(string(partialJSON), "evil.example") || strings.Contains(string(partialJSON), "super-secret") {
		t.Fatalf("partial probe report leaked provider operation or credential: %s", partialJSON)
	}

	reader.responses["aws-prod"] = struct {
		page provider.Page
		err  error
	}{err: errors.New("access token super-secret")}
	allFailed, err := runner.Run(context.Background(), []string{"aws-prod", "gcp-prod"})
	if err != nil || allFailed.Status != StatusNotReady {
		t.Fatalf("all-failed report = (%#v, %v), want not_ready report", allFailed, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled, err := runner.Run(ctx, []string{"aws-prod"})
	if err != nil || canceled.Status != StatusNotReady || canceled.Profiles[0].Error == nil || canceled.Profiles[0].Error.Code != "canceled" {
		t.Fatalf("canceled report = (%#v, %v), want safe canceled result", canceled, err)
	}
}

func TestRunnerDegradesWhenOnlyOneOfMultipleDeclaredScopesIsObserved(t *testing.T) {
	adapter := newProbeTestAdapter("aws-prod", model.ProviderAWS, "account", "account-1")
	adapter.status.Scopes = map[string]any{"accounts": []string{"account-1", "account-2"}}
	runner, reader := newProbeTestRunner(t, adapter)
	reader.responses["aws-prod"] = struct {
		page provider.Page
		err  error
	}{page: provider.Page{Rows: []map[string]any{{"scope": map[string]any{"account_id": "account-1"}}}, Scanned: 1, Requests: 1}}

	report, err := runner.Run(context.Background(), []string{"aws-prod"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	result := report.Profiles[0]
	if report.Status != StatusDegraded || result.Status != StatusDegraded || result.ScopeVerified || result.IdentityStatus != "authenticated_partial_scope_evidence" {
		t.Fatalf("multi-scope report = %#v, want degraded partial scope evidence", report)
	}
}

func TestRunnerDoesNotCrossMatchHuaweiProjectAndAccountScopeIDs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		declared      map[string]any
		observedScope map[string]any
	}{
		{
			name:          "configured project cannot match account row",
			declared:      map[string]any{"projects": []string{"shared-id"}},
			observedScope: map[string]any{"account_id": "shared-id"},
		},
		{
			name:          "configured account cannot match project row",
			declared:      map[string]any{"accounts": []string{"shared-id"}},
			observedScope: map[string]any{"project_id": "shared-id"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newProbeTestAdapter("huawei-prod", model.ProviderHuawei, "account", "shared-id")
			adapter.status.Scopes = tc.declared
			runner, reader := newProbeTestRunner(t, adapter)
			reader.responses["huawei-prod"] = struct {
				page provider.Page
				err  error
			}{page: provider.Page{Rows: []map[string]any{{"scope": tc.observedScope}}, Scanned: 1, Requests: 1}}

			report, err := runner.Run(context.Background(), []string{"huawei-prod"})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			result := report.Profiles[0]
			if result.Status != StatusDegraded || result.ScopeVerified || result.IdentityStatus != "authenticated_scope_unverified" {
				t.Fatalf("Huawei collision result = %#v, want unverified degraded scope", result)
			}
		})
	}
}

type coordinatorRunner struct {
	profiles []string
	report   Report
	err      error

	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (r *coordinatorRunner) Profiles() []string { return append([]string(nil), r.profiles...) }
func (r *coordinatorRunner) Run(ctx context.Context, _ []string) (Report, error) {
	r.mu.Lock()
	r.calls++
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	r.mu.Unlock()
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return Report{}, ctx.Err()
		}
	}
	return r.report, r.err
}
func (r *coordinatorRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestCoordinatorReusesTTLAndRefreshesAfterExpiry(t *testing.T) {
	base := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	clock := base
	runner := &coordinatorRunner{profiles: []string{"aws-prod"}, report: Report{ProbeID: "probe-1", Status: StatusReady, ObservedAt: base, Profiles: []ProfileResult{{Profile: "aws-prod", Status: StatusReady}}}}
	coordinator := NewCoordinator(runner, time.Minute, time.Second)
	coordinator.now = func() time.Time { return clock }

	first, err := coordinator.Current(context.Background())
	if err != nil || first.Cached || runner.callCount() != 1 {
		t.Fatalf("first Current() = (%#v, %v), want uncached first probe", first, err)
	}
	second, err := coordinator.Current(context.Background())
	if err != nil || !second.Cached || runner.callCount() != 1 {
		t.Fatalf("second Current() = (%#v, %v), want cached result without probe", second, err)
	}
	clock = base.Add(time.Minute + time.Nanosecond)
	third, err := coordinator.Current(context.Background())
	if err != nil || third.Cached || runner.callCount() != 2 {
		t.Fatalf("expired Current() = (%#v, %v), want refreshed uncached probe", third, err)
	}
}

func TestCoordinatorSingleFlightsConcurrentRefreshes(t *testing.T) {
	runner := &coordinatorRunner{
		profiles: []string{"aws-prod"},
		report:   Report{ProbeID: "probe-1", Status: StatusDegraded, Profiles: []ProfileResult{{Profile: "aws-prod", Status: StatusDegraded}}},
		started:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	coordinator := NewCoordinator(runner, time.Minute, time.Second)
	results := make(chan Report, 2)
	errorsOut := make(chan error, 2)
	go func() {
		report, err := coordinator.Current(context.Background())
		results <- report
		errorsOut <- err
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first readiness refresh did not start")
	}
	go func() {
		report, err := coordinator.Current(context.Background())
		results <- report
		errorsOut <- err
	}()
	close(runner.release)
	for i := 0; i < 2; i++ {
		if err := <-errorsOut; err != nil {
			t.Fatalf("Current() error = %v", err)
		}
	}
	if runner.callCount() != 1 {
		t.Fatalf("concurrent refresh calls = %d, want one probe", runner.callCount())
	}
	var cached int
	for i := 0; i < 2; i++ {
		if (<-results).Cached {
			cached++
		}
	}
	if cached != 1 {
		t.Fatalf("concurrent refresh cached results = %d, want one cached waiter result", cached)
	}
}
