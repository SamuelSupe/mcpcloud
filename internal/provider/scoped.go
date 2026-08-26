package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

var regionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type scopedAdapter struct {
	Adapter
	profile config.Profile
}

func newScopedAdapter(adapter Adapter, profile config.Profile) Adapter {
	return &scopedAdapter{Adapter: adapter, profile: profile}
}

func (a *scopedAdapter) ConfiguredRegions() []string {
	if contains(a.profile.Regions, "*") {
		return nil
	}
	return append([]string(nil), a.profile.Regions...)
}

// TargetRegions returns the regions that require separate provider calls. Cost
// APIs and GCP's global IAM/Monitoring endpoints are queried once; the engine
// applies an explicit DSL region scope to their normalized rows afterward.
func (a *scopedAdapter) TargetRegions(source model.Source, requested []string) ([]string, error) {
	for _, region := range requested {
		if err := a.validateRegion(region); err != nil {
			return nil, err
		}
	}
	if source == model.SourceCosts || (a.Provider() == model.ProviderGCP && (source == model.SourceIAM || source == model.SourceMetrics)) {
		return []string{""}, nil
	}

	regions := append([]string(nil), requested...)
	if len(regions) == 0 {
		if source == model.SourceMetrics && (a.Provider() == model.ProviderAzure || a.Provider() == model.ProviderTencent) {
			regions = exactRegions(a.profile.Regions)
		} else {
			regions = a.ConfiguredRegions()
		}
	}
	if source == model.SourceMetrics && (a.Provider() == model.ProviderAzure || a.Provider() == model.ProviderTencent) {
		regions = exactRegions(regions)
		if len(regions) == 0 {
			return nil, &Error{Code: "capability_unavailable", Message: fmt.Sprintf("%s metrics require at least one exact profile region", a.Provider())}
		}
	}
	if len(regions) == 0 {
		return []string{""}, nil
	}
	return regions, nil
}

func (a *scopedAdapter) ConfiguredScopes() []string {
	return a.ConfiguredScopesFor(model.SourceResources)
}

func (a *scopedAdapter) ConfiguredScopesFor(source model.Source) []string {
	var values []string
	switch a.Provider() {
	case model.ProviderGCP:
		if source == model.SourceMetrics {
			if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
				values = append(values, scope)
			}
			for _, project := range a.profile.Scopes.Projects {
				values = append(values, "projects/"+project)
			}
			break
		}
		if source == model.SourceCosts {
			if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
				values = append(values, scope)
			}
			for _, project := range a.profile.Scopes.Projects {
				values = append(values, "projects/"+project)
			}
			break
		}
		if scope := a.profile.Options["asset_scope"]; scope != "" {
			values = append(values, scope)
		}
		for _, organization := range a.profile.Scopes.Organizations {
			values = append(values, "organizations/"+organization)
		}
		for _, project := range a.profile.Scopes.Projects {
			values = append(values, "projects/"+project)
		}
	case model.ProviderAzure:
		values = append(values, a.profile.Scopes.Subscriptions...)
	case model.ProviderHuawei:
		if source == model.SourceCosts {
			values = append(values, a.profile.Scopes.Accounts...)
		} else if source == model.SourceMetrics {
			values = append(values, a.profile.Scopes.Projects...)
		} else {
			values = append(values, a.profile.Scopes.Projects...)
			if len(values) == 0 {
				values = append(values, a.profile.Scopes.Accounts...)
			}
		}
	default:
		values = append(values, a.profile.Scopes.Accounts...)
	}
	return unique(values)
}

func (a *scopedAdapter) Query(ctx context.Context, request QueryRequest) (Page, error) {
	if err := a.validateRegion(request.Region); err != nil {
		return Page{}, err
	}
	scopeIDs, err := a.scopeIDs(request.Source, request.Accounts)
	if err != nil {
		return Page{}, err
	}
	page, err := a.Adapter.Query(ctx, request)
	page.Rows = a.filterRows(page.Rows, request.Source, scopeIDs)
	return page, err
}

func (a *scopedAdapter) NativeRead(ctx context.Context, request NativeRequest) (Page, error) {
	if err := a.validateRegion(request.Region); err != nil {
		return Page{}, err
	}
	if region, ok := request.Params["region"].(string); ok {
		if err := a.validateRegion(region); err != nil {
			return Page{}, err
		}
	}
	page, err := a.Adapter.NativeRead(ctx, request)
	page.Rows = a.filterRows(page.Rows, model.SourceResources, a.ConfiguredScopesFor(model.SourceResources))
	return page, err
}

func (a *scopedAdapter) validateRegion(region string) error {
	if region == "" {
		return nil
	}
	if region != "*" && !regionPattern.MatchString(region) {
		return &Error{Code: "invalid_scope", Message: fmt.Sprintf("region %q is not a valid region identifier", region)}
	}
	if len(a.profile.Regions) == 0 || contains(a.profile.Regions, "*") {
		return nil
	}
	if !contains(a.profile.Regions, region) {
		return &Error{Code: "scope_not_allowed", Message: fmt.Sprintf("region %q is outside the profile allowlist", region)}
	}
	return nil
}

func (a *scopedAdapter) scopeIDs(source model.Source, requested []string) ([]string, error) {
	configured := a.ConfiguredScopesFor(source)
	if len(configured) == 0 {
		return append([]string(nil), requested...), nil
	}
	if len(requested) == 0 {
		return configured, nil
	}
	for _, value := range requested {
		if !contains(configured, value) {
			return nil, &Error{Code: "scope_not_allowed", Message: fmt.Sprintf("scope %q is outside the profile allowlist", value)}
		}
	}
	return append([]string(nil), requested...), nil
}

func (a *scopedAdapter) filterRows(rows []map[string]any, source model.Source, scopeIDs []string) []map[string]any {
	filtered := rows[:0]
	for _, row := range rows {
		if !a.serviceAllowed(row) || !a.rowRegionAllowed(row) || !a.rowScopeAllowed(row, source, scopeIDs) {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func (a *scopedAdapter) serviceAllowed(row map[string]any) bool {
	if len(a.profile.Services) == 0 || contains(a.profile.Services, "*") {
		return true
	}
	domain, _ := row["domain"].(string)
	service, _ := row["service"].(string)
	return contains(a.profile.Services, domain) || contains(a.profile.Services, service)
}

func (a *scopedAdapter) rowRegionAllowed(row map[string]any) bool {
	if len(a.profile.Regions) == 0 || contains(a.profile.Regions, "*") {
		return true
	}
	region, _ := row["region"].(string)
	return region == "" || contains(a.profile.Regions, region)
}

func exactRegions(regions []string) []string {
	result := make([]string, 0, len(regions))
	for _, region := range regions {
		if region != "" && region != "*" {
			result = append(result, region)
		}
	}
	return unique(result)
}

func (a *scopedAdapter) rowScopeAllowed(row map[string]any, source model.Source, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	scope, ok := row["scope"].(map[string]any)
	if !ok {
		return false
	}
	for _, candidate := range allowed {
		for _, key := range a.scopeKeys(source, candidate) {
			text, _ := scope[key].(string)
			if text != "" && (candidate == text || trimScopePrefix(candidate) == trimScopePrefix(text)) {
				return true
			}
		}
	}
	return false
}

func (a *scopedAdapter) scopeKeys(source model.Source, candidate string) []string {
	switch a.Provider() {
	case model.ProviderGCP:
		candidateID := trimScopePrefix(candidate)
		organization := strings.HasPrefix(candidate, "organizations/") || contains(a.profile.Scopes.Organizations, candidate) || contains(a.profile.Scopes.Organizations, candidateID)
		project := strings.HasPrefix(candidate, "projects/") || contains(a.profile.Scopes.Projects, candidate) || contains(a.profile.Scopes.Projects, candidateID)
		if configured := a.profile.Options["asset_scope"]; configured != "" && (configured == candidate || trimScopePrefix(configured) == candidateID) {
			organization = organization || strings.HasPrefix(configured, "organizations/")
			project = project || strings.HasPrefix(configured, "projects/")
		}
		switch {
		case organization && !project:
			return []string{"organization_id"}
		case project && !organization:
			return []string{"project_id"}
		default:
			return []string{"organization_id", "project_id"}
		}
	case model.ProviderAzure:
		return []string{"subscription_id"}
	case model.ProviderHuawei:
		if source == model.SourceCosts || len(a.profile.Scopes.Projects) == 0 {
			return []string{"account_id"}
		}
		return []string{"project_id"}
	default:
		return []string{"account_id"}
	}
}

func trimScopePrefix(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 && index+1 < len(value) {
		return value[index+1:]
	}
	return value
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
