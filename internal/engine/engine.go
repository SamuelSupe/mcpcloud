package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/dsl"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type Engine struct {
	registry      *provider.Registry
	limits        config.Limits
	cursors       *CursorStore
	providerSlots chan struct{}
	audit         AuditSink
	now           func() time.Time
}

type ExplainResult struct {
	Source           model.Source          `json:"source"`
	Profiles         []string              `json:"profiles"`
	Targets          []Target              `json:"targets"`
	Filter           string                `json:"filter,omitempty"`
	Pushdown         []string              `json:"pushdown,omitempty"`
	Residual         []string              `json:"residual,omitempty"`
	Fields           []string              `json:"fields,omitempty"`
	Limit            int                   `json:"limit"`
	Capabilities     []provider.Capability `json:"capabilities"`
	CapabilityErrors []string              `json:"capability_errors,omitempty"`
}

type Target struct {
	Profile  string         `json:"profile"`
	Provider model.Provider `json:"provider"`
	Scope    string         `json:"scope,omitempty"`
	Region   string         `json:"region,omitempty"`
	regions  []string
}

func New(registry *provider.Registry, limits config.Limits) *Engine {
	concurrency := limits.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	return &Engine{
		registry:      registry,
		limits:        limits,
		cursors:       NewCursorStore(limits.CursorTTL, 128),
		providerSlots: make(chan struct{}, concurrency),
		now:           time.Now,
	}
}

func (e *Engine) Parse(input string) (dsl.Query, error) { return dsl.Parse(input, e.now().UTC()) }

func (e *Engine) Explain(query dsl.Query) (ExplainResult, error) {
	targets, errors := e.targets(query)
	result := ExplainResult{Source: query.Source, Targets: targets, Filter: query.FilterText, Fields: query.Fields, Limit: query.Limit, Capabilities: e.registry.Capabilities(), CapabilityErrors: errors}
	for _, target := range targets {
		if !containsString(result.Profiles, target.Profile) {
			result.Profiles = append(result.Profiles, target.Profile)
		}
		adapter, _ := e.registry.Get(target.Profile)
		available := false
		for _, capability := range adapter.Capabilities() {
			if capability.Source == query.Source {
				available = capability.Status == "available" || capability.Status == "inventory"
				if !available {
					result.CapabilityErrors = append(result.CapabilityErrors, fmt.Sprintf("profile %q source %s: %s", target.Profile, query.Source, capability.Notes))
				}
				break
			}
		}
		if !available && !containsCapabilityError(result.CapabilityErrors, target.Profile, query.Source) {
			result.CapabilityErrors = append(result.CapabilityErrors, fmt.Sprintf("profile %q does not advertise source %s", target.Profile, query.Source))
		}
	}
	if query.Filter != nil {
		result.Residual = []string{query.Filter.String()}
		if query.Source == model.SourceMetrics {
			if selectors, ok := dsl.MetricSelectors(query.Filter); ok && len(selectors) > 0 {
				result.Pushdown = appendUnique(result.Pushdown, "where.metric")
			}
		}
	}
	if query.Range != nil {
		result.Pushdown = appendUnique(result.Pushdown, "range")
	}
	if query.Step > 0 {
		result.Pushdown = appendUnique(result.Pushdown, "step")
	}
	if len(query.Scope.Profiles) > 0 {
		result.Pushdown = append(result.Pushdown, "scope.profile")
	}
	if len(query.Scope.Providers) > 0 {
		result.Pushdown = append(result.Pushdown, "scope.provider")
	}
	if len(query.Scope.Accounts) > 0 {
		result.Pushdown = append(result.Pushdown, "scope.account")
	}
	if len(query.Scope.Regions) > 0 {
		for _, target := range targets {
			if target.Region == "" {
				result.Residual = appendUnique(result.Residual, "scope.region")
			} else {
				result.Pushdown = appendUnique(result.Pushdown, "scope.region")
			}
		}
	}
	if len(targets) > e.limits.MaxTargets {
		return result, fmt.Errorf("query expands to %d targets; maximum is %d", len(targets), e.limits.MaxTargets)
	}
	if err := e.validateTargetBudgets(len(targets)); err != nil {
		return result, err
	}
	return result, nil
}

func (e *Engine) Query(ctx context.Context, query dsl.Query, pageSize int) (model.QueryResult, error) {
	started := e.now()
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > e.limits.MaxPageSize {
		return model.QueryResult{}, fmt.Errorf("page_size exceeds maximum %d", e.limits.MaxPageSize)
	}
	targets, targetErrors := e.targets(query)
	if len(targets) == 0 {
		if len(targetErrors) > 0 {
			return model.QueryResult{}, fmt.Errorf("query has no executable targets: %s", strings.Join(targetErrors, "; "))
		}
		return model.QueryResult{}, fmt.Errorf("query has no matching configured profiles")
	}
	if len(targets) > e.limits.MaxTargets {
		return model.QueryResult{}, fmt.Errorf("query expands to %d targets; maximum is %d", len(targets), e.limits.MaxTargets)
	}
	if err := e.validateTargetBudgets(len(targets)); err != nil {
		return model.QueryResult{}, err
	}
	queryID := newID()
	type targetResult struct {
		target Target
		page   provider.Page
		err    error
	}
	results := make(chan targetResult, len(targets))
	var wg sync.WaitGroup
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case e.providerSlots <- struct{}{}:
				defer func() { <-e.providerSlots }()
			case <-ctx.Done():
				results <- targetResult{target: target, err: ctx.Err()}
				return
			}
			adapter, _ := e.registry.Get(target.Profile)
			request := provider.QueryRequest{Source: query.Source, Region: target.Region}
			metricSelectors := []string(nil)
			if query.Source == model.SourceMetrics {
				metricSelectors, _ = dsl.MetricSelectors(query.Filter)
			}
			if target.Scope != "" {
				request.Accounts = []string{target.Scope}
			}
			if query.Range != nil {
				request.Start, request.End = &query.Range.Start, &query.Range.End
			}
			request.Step = query.Step
			perTargetRequests := max(1, e.limits.MaxProviderRequests/len(targets))
			perTargetScan := max(1, e.limits.MaxScannedItems/len(targets))
			rowGoal := query.Limit
			if query.Filter != nil || len(query.Aggregates) > 0 || len(query.Sort) > 0 {
				rowGoal = perTargetScan
			}
			rowGoal = min(rowGoal, perTargetScan)
			requests := []provider.QueryRequest{request}
			if len(metricSelectors) > 0 {
				requests = make([]provider.QueryRequest, 0, len(metricSelectors))
				for _, selector := range metricSelectors {
					metricRequest := request
					metricRequest.Metrics = []string{selector}
					requests = append(requests, metricRequest)
				}
			}
			var combined provider.Page
			for requestIndex := range requests {
				request := requests[requestIndex]
				seenTokens := map[string]bool{}
				for combined.Requests < perTargetRequests && combined.Scanned < perTargetScan && len(combined.Rows) < rowGoal {
					request.Limit = min(e.limits.MaxPageSize, perTargetScan-combined.Scanned)
					request.Limit = min(request.Limit, rowGoal-len(combined.Rows))
					operation := operationForSource(adapter, query.Source)
					page, attempts, err := queryPageWithRetry(ctx, adapter, request, min(3, perTargetRequests-combined.Requests), func(attempt int, page provider.Page, err error, started time.Time) {
						e.recordAudit(ctx, AuditEvent{CorrelationID: queryID, Provider: target.Provider, Profile: target.Profile, Source: query.Source, Operation: operation, Scope: target.Scope, Region: target.Region, Attempt: attempt}, page, err, started)
					})
					combined.Requests += attempts
					if err != nil {
						results <- targetResult{target: target, page: combined, err: err}
						return
					}
					pageScanned := max(page.Scanned, len(page.Rows))
					scanRemaining := perTargetScan - combined.Scanned
					rowRemaining := rowGoal - len(combined.Rows)
					appendCount := min(len(page.Rows), min(scanRemaining, rowRemaining))
					combined.Rows = append(combined.Rows, page.Rows[:appendCount]...)
					combined.Scanned += min(pageScanned, scanRemaining)
					combined.NextToken = page.NextToken
					if pageScanned > scanRemaining || len(page.Rows) > appendCount {
						combined.NextToken = "local_execution_limit_reached"
					}
					if page.NextToken == "" || seenTokens[page.NextToken] {
						break
					}
					seenTokens[page.NextToken] = true
					request.PageToken = page.NextToken
				}
				if combined.Requests >= perTargetRequests || combined.Scanned >= perTargetScan || len(combined.Rows) >= rowGoal {
					if requestIndex+1 < len(requests) {
						combined.NextToken = "additional_metric_selectors_truncated"
					}
					break
				}
			}
			results <- targetResult{target: target, page: combined}
		}()
	}
	wg.Wait()
	close(results)
	var rows []map[string]any
	coverage := model.Coverage{Planned: len(targets) + len(targetErrors), Failed: len(targetErrors)}
	errorsOut := make([]model.QueryError, 0, len(targetErrors))
	for _, message := range targetErrors {
		errorsOut = append(errorsOut, model.QueryError{Code: "target_skipped", Message: message})
	}
	requests, scanned, visitedRows := 0, 0, 0
	truncated := false
	for result := range results {
		requests += result.page.Requests
		scanned += result.page.Scanned
		if result.err != nil {
			coverage.Failed++
			errorsOut = append(errorsOut, toQueryError(result.target, result.err))
		} else {
			coverage.Succeeded++
		}
		if result.page.NextToken != "" {
			truncated = true
		}
		for _, row := range result.page.Rows {
			if visitedRows >= e.limits.MaxScannedItems {
				truncated = true
				break
			}
			visitedRows++
			if !rowMatchesRegions(row, result.target.regions) {
				continue
			}
			if query.Filter != nil {
				ok, err := query.Filter.Eval(row)
				if err != nil {
					errorsOut = append(errorsOut, model.QueryError{Provider: result.target.Provider, Profile: result.target.Profile, Scope: result.target.Scope, Region: result.target.Region, Code: "filter_error", Message: sanitizeError(err.Error())})
					continue
				}
				if !ok {
					continue
				}
			}
			if !query.IncludeNative {
				delete(row, "native")
			}
			rows = append(rows, row)
		}
	}
	if len(query.Aggregates) > 0 {
		rows = aggregateRows(rows, query.Aggregates, query.GroupBy)
	}
	sortRows(rows, query.Sort)
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
		truncated = true
	}
	if scanned >= e.limits.MaxScannedItems || requests >= e.limits.MaxProviderRequests {
		truncated = true
	}
	rows = projectRows(rows, query.Fields)
	columns := inferColumns(rows, query.Fields)
	status := "ok"
	if coverage.Succeeded == 0 && len(rows) == 0 {
		status = "failed"
	} else if coverage.Failed > 0 || len(errorsOut) > 0 {
		status = "partial"
	}
	base := model.QueryResult{QueryID: queryID, Status: status, Columns: columns, Coverage: coverage, Errors: errorsOut, Stats: model.QueryStats{Requests: requests, Scanned: scanned, Truncated: truncated, DurationMS: e.now().Sub(started).Milliseconds(), ObservedAt: e.now().UTC()}}
	count := min(pageSize, len(rows))
	base.Rows = append([]map[string]any(nil), rows[:count]...)
	base.Stats.Returned = len(base.Rows)
	if count < len(rows) {
		base.NextCursor = e.cursors.Put(cursorState{rows: rows[count:], columns: columns, base: base, pageSize: pageSize})
	}
	return base, nil
}

func (e *Engine) Next(cursor string) (model.QueryResult, error) {
	if len(cursor) > 128 {
		return model.QueryResult{}, fmt.Errorf("cursor is invalid or expired")
	}
	result, ok := e.cursors.Next(cursor)
	if !ok {
		return model.QueryResult{}, fmt.Errorf("cursor is invalid or expired")
	}
	return result, nil
}

func (e *Engine) NativeRead(ctx context.Context, profile string, request provider.NativeRequest) (provider.Page, error) {
	return e.NativeReadWithID(ctx, newID(), profile, request)
}

func (e *Engine) NativeReadWithID(ctx context.Context, correlationID, profile string, request provider.NativeRequest) (provider.Page, error) {
	adapter, ok := e.registry.Get(profile)
	if !ok {
		return provider.Page{}, fmt.Errorf("unknown profile %q", profile)
	}
	if request.Limit <= 0 {
		request.Limit = 100
	}
	if request.Limit > e.limits.MaxPageSize {
		return provider.Page{}, fmt.Errorf("page size exceeds maximum %d", e.limits.MaxPageSize)
	}
	var selected *provider.Operation
	for _, operation := range adapter.Operations() {
		if operation.Name == request.Operation {
			operation := operation
			selected = &operation
			break
		}
	}
	if selected == nil {
		return provider.Page{}, fmt.Errorf("operation %q is not registered for profile %q", request.Operation, profile)
	}
	if err := selected.ValidateParams(request.Params); err != nil {
		return provider.Page{}, err
	}
	select {
	case e.providerSlots <- struct{}{}:
		defer func() { <-e.providerSlots }()
	case <-ctx.Done():
		return provider.Page{}, ctx.Err()
	}
	if correlationID == "" {
		correlationID = newID()
	}
	page, requests, err := readPageWithRetry(ctx, min(3, e.limits.MaxProviderRequests), func() (provider.Page, error) {
		return adapter.NativeRead(ctx, request)
	}, func(attempt int, page provider.Page, err error, started time.Time) {
		e.recordAudit(ctx, AuditEvent{CorrelationID: correlationID, Provider: adapter.Provider(), Profile: profile, Source: sourceForOperation(adapter, request.Operation), Operation: request.Operation, Scope: scopeForNative(request.Params), Region: request.Region, Attempt: attempt}, page, err, started)
	})
	page.Requests = requests
	if err == nil {
		return page, nil
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return page, &provider.Error{Code: providerErr.Code, Message: sanitizeError(providerErr.Message), Operation: providerErr.Operation, Retryable: providerErr.Retryable}
	}
	return page, errors.New(sanitizeError(err.Error()))
}

func (e *Engine) validateTargetBudgets(targets int) error {
	if targets > e.limits.MaxProviderRequests {
		return fmt.Errorf("query expands to %d targets; provider request budget is %d", targets, e.limits.MaxProviderRequests)
	}
	if targets > e.limits.MaxScannedItems {
		return fmt.Errorf("query expands to %d targets; scanned item budget is %d", targets, e.limits.MaxScannedItems)
	}
	return nil
}

func (e *Engine) targets(query dsl.Query) ([]Target, []string) {
	wantedProfiles := query.Scope.Profiles
	if len(wantedProfiles) == 0 {
		wantedProfiles = e.registry.Profiles()
	}
	var targets []Target
	var problems []string
	for _, name := range wantedProfiles {
		adapter, ok := e.registry.Get(name)
		if !ok {
			problems = append(problems, fmt.Sprintf("profile %q is not configured", name))
			continue
		}
		if len(query.Scope.Providers) > 0 && !containsProvider(query.Scope.Providers, adapter.Provider()) {
			continue
		}
		regions := append([]string(nil), query.Scope.Regions...)
		if resolver, ok := adapter.(interface {
			TargetRegions(model.Source, []string) ([]string, error)
		}); ok {
			resolved, err := resolver.TargetRegions(query.Source, regions)
			if err != nil {
				problems = append(problems, fmt.Sprintf("profile %q: %s", name, sanitizeError(err.Error())))
				continue
			}
			regions = resolved
		} else {
			if query.Source == model.SourceCosts {
				regions = []string{""}
			}
			if len(regions) == 0 {
				if configured, ok := adapter.(interface{ ConfiguredRegions() []string }); ok {
					regions = configured.ConfiguredRegions()
				}
				if len(regions) == 0 {
					regions = []string{""}
				}
			}
		}
		scopes := query.Scope.Accounts
		if len(scopes) == 0 {
			if configured, ok := adapter.(interface {
				ConfiguredScopesFor(model.Source) []string
			}); ok {
				scopes = configured.ConfiguredScopesFor(query.Source)
			} else if configured, ok := adapter.(interface{ ConfiguredScopes() []string }); ok {
				scopes = configured.ConfiguredScopes()
			}
			if len(scopes) == 0 {
				scopes = []string{""}
			}
		}
		for _, region := range regions {
			for _, scope := range scopes {
				targets = append(targets, Target{Profile: name, Provider: adapter.Provider(), Scope: scope, Region: region, regions: append([]string(nil), query.Scope.Regions...)})
				if len(targets) > e.limits.MaxTargets {
					return targets, problems
				}
			}
		}
	}
	return targets, problems
}

func rowMatchesRegions(row map[string]any, regions []string) bool {
	if len(regions) == 0 || containsString(regions, "*") {
		return true
	}
	region, _ := row["region"].(string)
	return containsString(regions, region)
}

func appendUnique(values []string, value string) []string {
	if containsString(values, value) {
		return values
	}
	return append(values, value)
}

func toQueryError(target Target, err error) model.QueryError {
	if errors.Is(err, context.DeadlineExceeded) {
		return model.QueryError{Provider: target.Provider, Profile: target.Profile, Scope: target.Scope, Region: target.Region, Code: "timeout", Message: "provider request deadline exceeded", Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return model.QueryError{Provider: target.Provider, Profile: target.Profile, Scope: target.Scope, Region: target.Region, Code: "canceled", Message: "provider request canceled"}
	}
	result := model.QueryError{Provider: target.Provider, Profile: target.Profile, Scope: target.Scope, Region: target.Region, Code: "provider_error", Message: sanitizeError(err.Error())}
	if pe, ok := err.(*provider.Error); ok {
		result.Code, result.Message, result.Operation, result.Retryable = pe.Code, sanitizeError(pe.Message), pe.Operation, pe.Retryable
	}
	return result
}

func containsCapabilityError(messages []string, profile string, source model.Source) bool {
	prefix := fmt.Sprintf("profile %q", profile)
	for _, message := range messages {
		if strings.Contains(message, prefix) && strings.Contains(message, string(source)) {
			return true
		}
	}
	return false
}

func sanitizeError(message string) string {
	lower := strings.ToLower(message)
	for _, marker := range []string{"secret", "token", "password", "credential", "connection_string", "access_key", "access key", "accesskey", "private key", "authorization", "signature"} {
		if strings.Contains(lower, marker) {
			return "provider returned a sensitive error; details were redacted"
		}
	}
	if len(message) > 512 {
		return message[:512] + "…"
	}
	return message
}

func aggregateRows(rows []map[string]any, aggregates []dsl.Aggregate, groupBy []string) []map[string]any {
	type accumulator struct {
		row         map[string]any
		count       map[string]int
		initialized map[string]bool
	}
	groups := map[string]*accumulator{}
	for _, row := range rows {
		keyParts := make([]string, len(groupBy))
		groupValues := map[string]any{}
		for i, field := range groupBy {
			value, _ := dsl.ValueAt(row, field)
			keyParts[i] = fmt.Sprint(value)
			groupValues[field] = value
		}
		key := strings.Join(keyParts, "\x00")
		acc := groups[key]
		if acc == nil {
			acc = &accumulator{row: groupValues, count: map[string]int{}, initialized: map[string]bool{}}
			groups[key] = acc
		}
		for _, agg := range aggregates {
			if agg.Function == "count" {
				if agg.Field != "*" {
					value, ok := dsl.ValueAt(row, agg.Field)
					if !ok || value == nil {
						continue
					}
				}
				acc.count[agg.Alias]++
				acc.row[agg.Alias] = acc.count[agg.Alias]
				continue
			}
			value, ok := dsl.ValueAt(row, agg.Field)
			if !ok {
				continue
			}
			n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
			if err != nil {
				continue
			}
			switch agg.Function {
			case "sum", "avg":
				current, _ := acc.row[agg.Alias].(float64)
				acc.row[agg.Alias] = current + n
				acc.count[agg.Alias]++
			case "min":
				current, _ := acc.row[agg.Alias].(float64)
				if !acc.initialized[agg.Alias] || n < current {
					acc.row[agg.Alias] = n
				}
				acc.initialized[agg.Alias] = true
			case "max":
				current, _ := acc.row[agg.Alias].(float64)
				if !acc.initialized[agg.Alias] || n > current {
					acc.row[agg.Alias] = n
				}
				acc.initialized[agg.Alias] = true
			}
		}
	}
	if len(rows) == 0 && len(groupBy) == 0 {
		row := map[string]any{}
		for _, agg := range aggregates {
			if agg.Function == "count" {
				row[agg.Alias] = 0
			}
		}
		if len(row) > 0 {
			return []map[string]any{row}
		}
	}
	result := make([]map[string]any, 0, len(groups))
	for _, acc := range groups {
		for _, agg := range aggregates {
			if agg.Function == "avg" && acc.count[agg.Alias] > 0 {
				acc.row[agg.Alias] = acc.row[agg.Alias].(float64) / float64(acc.count[agg.Alias])
			}
		}
		result = append(result, acc.row)
	}
	return result
}

func sortRows(rows []map[string]any, fields []dsl.SortField) {
	if len(fields) == 0 {
		return
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, field := range fields {
			a, _ := dsl.ValueAt(rows[i], field.Field)
			b, _ := dsl.ValueAt(rows[j], field.Field)
			comparison := compareSortValues(a, b)
			if comparison == 0 {
				continue
			}
			if field.Direction == "desc" {
				return comparison > 0
			}
			return comparison < 0
		}
		return false
	})
}

func compareSortValues(a, b any) int {
	af, aerr := strconv.ParseFloat(fmt.Sprint(a), 64)
	bf, berr := strconv.ParseFloat(fmt.Sprint(b), 64)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	as, bs := fmt.Sprint(a), fmt.Sprint(b)
	return strings.Compare(as, bs)
}

type attemptObserver func(attempt int, page provider.Page, err error, started time.Time)

func queryPageWithRetry(ctx context.Context, adapter provider.Adapter, request provider.QueryRequest, maxRequests int, observer attemptObserver) (provider.Page, int, error) {
	return readPageWithRetry(ctx, maxRequests, func() (provider.Page, error) {
		return adapter.Query(ctx, request)
	}, observer)
}

func readPageWithRetry(ctx context.Context, maxRequests int, read func() (provider.Page, error), observer attemptObserver) (provider.Page, int, error) {
	if maxRequests < 1 {
		maxRequests = 1
	}
	requests := 0
	attempt := 0
	for {
		attempt++
		started := time.Now()
		page, err := read()
		if observer != nil {
			observer(attempt, page, err, started)
		}
		requests += max(1, page.Requests)
		if err == nil {
			return page, requests, nil
		}
		var providerErr *provider.Error
		if !errors.As(err, &providerErr) || !providerErr.Retryable || requests >= maxRequests {
			return page, requests, err
		}
		delay := time.Duration(100*(1<<(requests-1))) * time.Millisecond
		if delay > time.Second {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return provider.Page{}, requests, ctx.Err()
		case <-timer.C:
		}
	}
}

func projectRows(rows []map[string]any, fields []string) []map[string]any {
	if len(fields) == 0 {
		return rows
	}
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		projected := map[string]any{}
		for _, field := range fields {
			if value, ok := dsl.ValueAt(row, field); ok {
				projected[field] = value
			}
		}
		result = append(result, projected)
	}
	return result
}

func inferColumns(rows []map[string]any, preferred []string) []string {
	if len(preferred) > 0 {
		return append([]string(nil), preferred...)
	}
	set := map[string]bool{}
	var columns []string
	for _, row := range rows {
		for key := range row {
			if !set[key] {
				set[key] = true
				columns = append(columns, key)
			}
		}
	}
	sort.Strings(columns)
	return columns
}

func containsProvider(items []model.Provider, wanted model.Provider) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func NewCorrelationID() string { return newID() }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
