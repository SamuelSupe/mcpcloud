package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mcpcloud/internal/engine"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	StatusReady    = "ready"
	StatusDegraded = "degraded"
	StatusNotReady = "not_ready"
	probePageSize  = 100
	probeMaxPages  = 10
)

type CapabilityStatus struct {
	Source model.Source `json:"source"`
	Status string       `json:"status"`
}

type SafeError struct {
	Code      string `json:"code"`
	Operation string `json:"operation,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type ProfileResult struct {
	Profile        string              `json:"profile"`
	Provider       model.Provider      `json:"provider"`
	Status         string              `json:"status"`
	Authenticated  bool                `json:"authenticated"`
	ScopeVerified  bool                `json:"scope_verified"`
	IdentityStatus string              `json:"identity_status"`
	DeclaredScopes map[string][]string `json:"declared_scopes,omitempty"`
	ObservedScopes map[string][]string `json:"observed_scopes,omitempty"`
	Region         string              `json:"region,omitempty"`
	Operation      string              `json:"operation,omitempty"`
	Capabilities   []CapabilityStatus  `json:"capabilities,omitempty"`
	Error          *SafeError          `json:"error,omitempty"`
	ObservedAt     time.Time           `json:"observed_at"`
}

type Report struct {
	ProbeID    string          `json:"probe_id"`
	Status     string          `json:"status"`
	Cached     bool            `json:"cached"`
	ObservedAt time.Time       `json:"observed_at"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
	Profiles   []ProfileResult `json:"profiles"`
}

type NativeReader interface {
	NativeReadWithID(context.Context, string, string, provider.NativeRequest) (provider.Page, error)
}

type Runner struct {
	registry *provider.Registry
	reader   NativeReader
	now      func() time.Time
}

func NewRunner(registry *provider.Registry, reader NativeReader) *Runner {
	return &Runner{registry: registry, reader: reader, now: time.Now}
}

func (r *Runner) Profiles() []string {
	return r.registry.Profiles()
}

func (r *Runner) Run(ctx context.Context, profiles []string) (Report, error) {
	probeID, err := randomID()
	if err != nil {
		return Report{}, fmt.Errorf("create probe id: %w", err)
	}
	profiles = append([]string(nil), profiles...)
	sort.Strings(profiles)
	if len(profiles) == 0 {
		return Report{}, errors.New("no profiles selected")
	}
	for index := 1; index < len(profiles); index++ {
		if profiles[index] == profiles[index-1] {
			return Report{}, fmt.Errorf("profile %q is selected more than once", profiles[index])
		}
	}
	for _, name := range profiles {
		if _, ok := r.registry.Get(name); !ok {
			return Report{}, fmt.Errorf("unknown profile %q", name)
		}
	}
	observedAt := r.now().UTC()
	report := Report{ProbeID: probeID, Status: StatusNotReady, ObservedAt: observedAt, Profiles: make([]ProfileResult, 0, len(profiles))}
	ready, reachable := 0, 0
	for _, name := range profiles {
		adapter, _ := r.registry.Get(name)
		result := r.probeProfile(ctx, probeID, adapter, observedAt)
		report.Profiles = append(report.Profiles, result)
		if result.Status == StatusReady {
			ready++
			reachable++
		} else if result.Status == StatusDegraded {
			reachable++
		}
	}
	switch {
	case ready == len(report.Profiles):
		report.Status = StatusReady
	case reachable > 0:
		report.Status = StatusDegraded
	default:
		report.Status = StatusNotReady
	}
	return report, nil
}

func (r *Runner) probeProfile(ctx context.Context, probeID string, adapter provider.Adapter, observedAt time.Time) ProfileResult {
	result := ProfileResult{
		Profile: adapter.Profile(), Provider: adapter.Provider(), Status: StatusNotReady,
		IdentityStatus: "unverified", ObservedAt: observedAt,
		Capabilities: capabilityStatuses(adapter.Capabilities()),
	}
	readiness := adapter.Readiness(ctx)
	result.DeclaredScopes = declaredScopes(readiness.Scopes)
	if !readiness.Ready {
		result.Error = &SafeError{Code: readiness.Status}
		return result
	}
	operation, ok := inventoryOperation(adapter)
	if !ok {
		result.Error = &SafeError{Code: "probe_operation_unavailable"}
		return result
	}
	result.Operation = operation.Name
	for _, region := range probeRegions(adapter) {
		result.Region = region
		request := provider.NativeRequest{Operation: operation.Name, Region: region, Limit: probePageSize}
		for pageNumber := 0; pageNumber < probeMaxPages; pageNumber++ {
			page, err := r.reader.NativeReadWithID(ctx, probeID, adapter.Profile(), request)
			if err != nil {
				result.Error = safeError(err, operation.Name)
				return result
			}
			result.Authenticated = true
			result.ObservedScopes = mergeScopes(result.ObservedScopes, observedScopes(page.Rows))
			result.ScopeVerified, _ = scopeEvidence(adapter, result.DeclaredScopes, result.ObservedScopes)
			if result.ScopeVerified {
				result.Status = StatusReady
				result.IdentityStatus = "scope_verified"
				return result
			}
			if page.NextToken == "" || page.NextToken == request.PageToken {
				break
			}
			request.PageToken = page.NextToken
		}
	}
	var partialScopeEvidence bool
	result.ScopeVerified, partialScopeEvidence = scopeEvidence(adapter, result.DeclaredScopes, result.ObservedScopes)
	result.Status = StatusDegraded
	if partialScopeEvidence {
		result.IdentityStatus = "authenticated_partial_scope_evidence"
	} else {
		result.IdentityStatus = "authenticated_scope_unverified"
	}
	return result
}

func inventoryOperation(adapter provider.Adapter) (provider.Operation, bool) {
	operations := adapter.Operations()
	byName := make(map[string]provider.Operation, len(operations))
	for _, operation := range operations {
		byName[operation.Name] = operation
	}
	for _, capability := range adapter.Capabilities() {
		if capability.Source != model.SourceResources || len(capability.Operations) == 0 {
			continue
		}
		// A resource capability may lead with a DSL-only aggregate operation.
		// Readiness uses the first registered native read instead, so an optional
		// inventory backend (for example Tencent Resource Center) cannot make a
		// direct-product profile appear unavailable.
		for _, name := range capability.Operations {
			if operation, ok := byName[name]; ok {
				return operation, true
			}
		}
	}
	return provider.Operation{}, false
}

func capabilityStatuses(capabilities []provider.Capability) []CapabilityStatus {
	result := make([]CapabilityStatus, 0, len(capabilities))
	for _, capability := range capabilities {
		result = append(result, CapabilityStatus{Source: capability.Source, Status: capability.Status})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Source < result[j].Source })
	return result
}

func probeRegions(adapter provider.Adapter) []string {
	configured, ok := adapter.(interface{ ConfiguredRegions() []string })
	if !ok {
		return []string{""}
	}
	regions := make([]string, 0)
	for _, region := range configured.ConfiguredRegions() {
		if region != "" && region != "*" {
			regions = append(regions, region)
		}
	}
	if len(regions) == 0 {
		return []string{""}
	}
	return regions
}

func declaredScopes(raw map[string]any) map[string][]string {
	result := map[string][]string{}
	for _, key := range []string{"organizations", "tenants", "accounts", "projects", "subscriptions"} {
		switch values := raw[key].(type) {
		case []string:
			result[key] = unique(values, 64)
		case []any:
			items := make([]string, 0, len(values))
			for _, item := range values {
				if text, ok := item.(string); ok {
					items = append(items, text)
				}
			}
			result[key] = unique(items, 64)
		}
	}
	return compactScopes(result)
}

func observedScopes(rows []map[string]any) map[string][]string {
	result := map[string][]string{}
	for _, row := range rows {
		scope, _ := row["scope"].(map[string]any)
		for _, key := range []string{"organization_id", "tenant_id", "account_id", "project_id", "subscription_id"} {
			if value, ok := scope[key].(string); ok && value != "" {
				result[key] = append(result[key], value)
			}
		}
	}
	for key, values := range result {
		result[key] = unique(values, 64)
	}
	return compactScopes(result)
}

func mergeScopes(left, right map[string][]string) map[string][]string {
	merged := map[string][]string{}
	for key, values := range left {
		merged[key] = append([]string(nil), values...)
	}
	for key, values := range right {
		merged[key] = append(merged[key], values...)
	}
	for key, values := range merged {
		merged[key] = unique(values, 64)
	}
	return compactScopes(merged)
}

func compactScopes(scopes map[string][]string) map[string][]string {
	for key, values := range scopes {
		if len(values) == 0 {
			delete(scopes, key)
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	return scopes
}

func scopeEvidence(adapter provider.Adapter, declared, observed map[string][]string) (verified, partial bool) {
	configured, ok := adapter.(interface {
		ConfiguredScopesFor(model.Source) []string
	})
	if !ok {
		return false, false
	}
	targets := configured.ConfiguredScopesFor(model.SourceResources)
	matched := 0
	for _, target := range targets {
		if observedScopeMatches(adapter.Provider(), target, declared, observed) {
			matched++
		}
	}
	return len(targets) == 1 && matched == 1, matched > 0
}

func observedScopeMatches(providerName model.Provider, target string, declared, observed map[string][]string) bool {
	keys := []string{"account_id"}
	switch providerName {
	case model.ProviderGCP:
		switch {
		case strings.HasPrefix(target, "organizations/"):
			keys = []string{"organization_id"}
		case strings.HasPrefix(target, "projects/"):
			keys = []string{"project_id"}
		default:
			keys = []string{"organization_id", "project_id"}
		}
	case model.ProviderAzure:
		keys = []string{"subscription_id"}
	case model.ProviderHuawei:
		keys = []string{"account_id"}
		if scopeListContains(declared["projects"], target) {
			keys = []string{"project_id"}
		}
	}
	for _, key := range keys {
		for _, actual := range observed[key] {
			if scopeTail(target) == scopeTail(actual) {
				return true
			}
		}
	}
	return false
}

func scopeListContains(values []string, target string) bool {
	for _, value := range values {
		if scopeTail(value) == scopeTail(target) {
			return true
		}
	}
	return false
}

func safeError(err error, operation string) *SafeError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &SafeError{Code: "timeout", Operation: operation, Retryable: true}
	}
	if errors.Is(err, context.Canceled) {
		return &SafeError{Code: "canceled", Operation: operation}
	}
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		return &SafeError{Code: safeCode(providerErr.Code), Operation: operation, Retryable: providerErr.Retryable}
	}
	return &SafeError{Code: "provider_error", Operation: operation}
}

func safeCode(value string) string {
	if value == "" {
		return "provider_error"
	}
	if len(value) > 128 {
		value = value[:128]
	}
	var result strings.Builder
	result.Grow(len(value))
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-', char == '.', char == ':':
			result.WriteRune(char)
		default:
			result.WriteByte('_')
		}
	}
	return result.String()
}

func unique(values []string, limit int) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || contains(result, value) {
			continue
		}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	sort.Strings(result)
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func scopeTail(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

var _ NativeReader = (*engine.Engine)(nil)
