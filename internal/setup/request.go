package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const maxRequestBytes = 64 << 10

// ConfigurationRequest contains the same non-secret profile fields as YAML.
// Features select prerequisite checks, not additional cloud permissions.
type ConfigurationRequest struct {
	Name     string         `json:"name"`
	Profile  config.Profile `json:"profile"`
	Features []string       `json:"features,omitempty"`
}

// InputError is safe to return to an agent: it never includes rejected values.
type InputError struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (e *InputError) Error() string                { return e.Message }
func inputError(code, field, message string) error { return &InputError{code, field, message} }

// ReadConfigurationRequest accepts exactly one bounded JSON object. Parser
// diagnostics are deliberately not forwarded: unknown keys may contain secrets.
func ReadConfigurationRequest(r io.Reader) (ConfigurationRequest, error) {
	var request ConfigurationRequest
	data, err := io.ReadAll(io.LimitReader(r, maxRequestBytes+1))
	if err != nil {
		return request, inputError("input_unreadable", "input", "Cannot read the JSON request.")
	}
	if len(data) > maxRequestBytes {
		return request, inputError("input_too_large", "input", "JSON request must not exceed 64 KiB.")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return ConfigurationRequest{}, inputError("invalid_json", "input", "Expected one JSON object with name, profile and optional features; unknown fields are rejected.")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ConfigurationRequest{}, inputError("invalid_json", "input", "Expected exactly one JSON object.")
	}
	return request, nil
}

var setupNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var setupScopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)

// ValidateIdentity validates references before any discovery API is called. GCP
// discovery intentionally does not require a resource scope yet.
func ValidateIdentity(request ConfigurationRequest) error {
	if !setupNamePattern.MatchString(request.Name) {
		return inputError("invalid_profile_name", "name", "Use 1-64 letters, digits, underscores or hyphens, starting with a letter or digit.")
	}
	if !slices.Contains(model.Providers, request.Profile.Provider) {
		return inputError("invalid_provider", "profile.provider", "Select a supported cloud provider.")
	}
	if !request.Profile.IsEnabled() {
		return inputError("disabled_profile", "profile.enabled", "Setup adds an enabled profile.")
	}
	if request.Profile.Credential.Source == "" {
		return inputError("missing_credential_source", "profile.credential.source", "Specify an existing credential source; secret values are not accepted.")
	}
	cfg := config.Default()
	cfg.Profiles = map[string]config.Profile{request.Name: request.Profile}
	if err := cfg.Validate(); err != nil {
		return inputError("invalid_profile", "profile", "Profile fields are invalid. Check credential references, scopes, regions and provider options against the configuration guide.")
	}
	return nil
}

func addFeatureServices(p *config.Profile, features []string) {
	for _, item := range []struct{ feature, service string }{{"metrics", "monitoring"}, {"costs", "cost"}} {
		if slices.Contains(features, item.feature) && !slices.Contains(p.Services, item.service) {
			p.Services = append(p.Services, item.service)
		}
	}
}

// PrepareRequest applies the wizard's feature defaults and exact-scope boundary,
// then uses the existing configuration validator and provider capabilities.
func PrepareRequest(request ConfigurationRequest) (ConfigurationRequest, error) {
	if err := ValidateIdentity(request); err != nil {
		return ConfigurationRequest{}, err
	}
	p := request.Profile
	if len(p.Regions) == 0 || slices.Contains(p.Regions, "*") {
		return ConfigurationRequest{}, inputError("explicit_regions_required", "profile.regions", "Provide at least one exact region; setup does not allow wildcard regions.")
	}
	if len(p.Services) == 0 {
		return ConfigurationRequest{}, inputError("explicit_services_required", "profile.services", "Choose one or more normalized resource domains.")
	}
	for _, service := range p.Services {
		if !slices.Contains(provider.CoreDomains, service) {
			return ConfigurationRequest{}, inputError("invalid_service", "profile.services", "Services must be normalized domains listed in the setup guide.")
		}
	}
	for _, values := range [][]string{p.Scopes.Accounts, p.Scopes.Projects, p.Scopes.Subscriptions, p.Scopes.Organizations, p.Scopes.Tenants} {
		for _, value := range values {
			if !setupScopePattern.MatchString(value) {
				return ConfigurationRequest{}, inputError("explicit_scopes_required", "profile.scopes", "Scope entries must be explicit IDs, without wildcards or URLs.")
			}
		}
	}
	features := []string{"resources"}
	for _, feature := range request.Features {
		if !slices.Contains([]string{"resources", "metrics", "costs"}, feature) {
			return ConfigurationRequest{}, inputError("invalid_feature", "features", "Features may contain resources, metrics and costs.")
		}
		if !slices.Contains(features, feature) {
			features = append(features, feature)
		}
	}
	// Copy before appending so preparing a preview cannot mutate the caller's input.
	p.Services = append([]string(nil), p.Services...)
	addFeatureServices(&p, features)
	cfg := config.Default()
	cfg.Profiles = map[string]config.Profile{request.Name: p}
	registry, err := provider.FromConfig(cfg)
	if err != nil {
		return ConfigurationRequest{}, inputError("invalid_provider_configuration", "profile", "Provider settings are invalid; check scope IDs and provider options.")
	}
	adapter, _ := registry.Get(request.Name)
	scoped, ok := adapter.(interface{ ConfiguredScopesFor(model.Source) []string })
	if !ok || len(scoped.ConfiguredScopesFor(model.SourceResources)) == 0 {
		return ConfigurationRequest{}, inputError("explicit_scopes_required", "profile.scopes", "Provide the provider's account, project, subscription, organization or asset_scope.")
	}
	for _, feature := range features {
		if len(scoped.ConfiguredScopesFor(model.Source(feature))) == 0 {
			return ConfigurationRequest{}, inputError("feature_scope_required", "profile.scopes", "Provide an explicit scope for each selected feature; billing may need an account or project separate from resource inventory.")
		}
	}
	for _, capability := range adapter.Capabilities() {
		if slices.Contains(features, string(capability.Source)) && capability.Status == "not_configured" {
			return ConfigurationRequest{}, inputError("feature_not_configured", fmt.Sprintf("features.%s", capability.Source), capability.Notes)
		}
	}
	request.Profile = p
	request.Features = features
	return request, nil
}
