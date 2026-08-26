package provider

import (
	"context"
	"reflect"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

type registryAdapter struct {
	profile      string
	provider     model.Provider
	capabilities []Capability
	operations   []Operation
}

type rowsAdapter struct {
	registryAdapter
	page Page
}

func (a registryAdapter) Provider() model.Provider   { return a.provider }
func (a registryAdapter) Profile() string            { return a.profile }
func (a registryAdapter) Capabilities() []Capability { return a.capabilities }
func (a registryAdapter) Operations() []Operation    { return a.operations }
func (a registryAdapter) Readiness(context.Context) model.ProfileStatus {
	return model.ProfileStatus{Name: a.profile, Provider: a.provider, Ready: true, Status: "ready"}
}
func (a registryAdapter) Query(context.Context, QueryRequest) (Page, error) { return Page{}, nil }
func (a registryAdapter) NativeRead(context.Context, NativeRequest) (Page, error) {
	return Page{}, nil
}

func (a rowsAdapter) Query(context.Context, QueryRequest) (Page, error) {
	page := a.page
	page.Rows = append([]map[string]any(nil), a.page.Rows...)
	return page, nil
}

func TestRegistryProfilesCapabilitiesAndOperationsAreStable(t *testing.T) {
	awsDescribe := Operation{Name: "aws.ec2.describe_instances", Provider: model.ProviderAWS, Service: "ec2"}
	awsList := Operation{Name: "aws.ec2.list_regions", Provider: model.ProviderAWS, Service: "ec2"}
	gcpDescribe := Operation{Name: "gcp.compute.list_instances", Provider: model.ProviderGCP, Service: "compute"}
	registry, err := NewRegistry(
		registryAdapter{profile: "gcp-prod", provider: model.ProviderGCP, capabilities: []Capability{{Provider: model.ProviderGCP, Source: model.SourceResources, Status: "ready"}}, operations: []Operation{gcpDescribe}},
		registryAdapter{profile: "aws-prod", provider: model.ProviderAWS, capabilities: []Capability{{Provider: model.ProviderAWS, Source: model.SourceResources, Status: "ready"}}, operations: []Operation{awsList, awsDescribe}},
		registryAdapter{profile: "aws-stage", provider: model.ProviderAWS, capabilities: []Capability{{Provider: model.ProviderAWS, Source: model.SourceIAM, Status: "ready"}}, operations: []Operation{awsDescribe}},
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	if got, want := registry.Profiles(), []string{"aws-prod", "aws-stage", "gcp-prod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Profiles() = %#v, want %#v", got, want)
	}
	operations := registry.Operations()
	if got, want := []string{operations[0].Name, operations[1].Name, operations[2].Name}, []string{"aws.ec2.describe_instances", "aws.ec2.list_regions", "gcp.compute.list_instances"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Operations() names = %#v, want %#v", got, want)
	}
	if len(operations) != 3 {
		t.Fatalf("Operations() returned %d operations, want duplicate operation removed", len(operations))
	}
	if got := len(registry.Capabilities()); got != 3 {
		t.Fatalf("Capabilities() returned %d entries, want one per adapter capability", got)
	}
}

func TestNewRegistryRejectsEmptyAndDuplicateProfiles(t *testing.T) {
	base := registryAdapter{profile: "aws-prod", provider: model.ProviderAWS}
	for _, tc := range []struct {
		name     string
		adapters []Adapter
		want     string
	}{
		{name: "empty profile", adapters: []Adapter{registryAdapter{provider: model.ProviderAWS}}, want: "empty profile"},
		{name: "duplicate profile", adapters: []Adapter{base, base}, want: "duplicate provider profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewRegistry(tc.adapters...)
			if err == nil || !containsSubstring(err.Error(), tc.want) {
				t.Fatalf("NewRegistry() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestScopedAdapterConfiguredScopesForHuaweiBySource(t *testing.T) {
	scoped := newScopedAdapter(
		registryAdapter{profile: "huawei-prod", provider: model.ProviderHuawei},
		config.Profile{
			Provider: model.ProviderHuawei,
			Scopes: config.Scopes{
				Projects: []string{"project-a"},
				Accounts: []string{"account-a"},
			},
		},
	).(*scopedAdapter)

	for _, tt := range []struct {
		source model.Source
		want   []string
	}{
		{source: model.SourceResources, want: []string{"project-a"}},
		{source: model.SourceMetrics, want: []string{"project-a"}},
		{source: model.SourceCosts, want: []string{"account-a"}},
	} {
		if got := scoped.ConfiguredScopesFor(tt.source); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ConfiguredScopesFor(%q) = %#v, want %#v", tt.source, got, tt.want)
		}
	}
}

func TestScopedAdapterDoesNotFallbackHuaweiMetricScopesToAccounts(t *testing.T) {
	scoped := newScopedAdapter(
		registryAdapter{profile: "huawei-prod", provider: model.ProviderHuawei},
		config.Profile{
			Provider: model.ProviderHuawei,
			Scopes: config.Scopes{
				Accounts: []string{"account-a"},
			},
		},
	).(*scopedAdapter)

	if got := scoped.ConfiguredScopesFor(model.SourceMetrics); len(got) != 0 {
		t.Fatalf("ConfiguredScopesFor(metrics) = %#v, want no account fallback", got)
	}
}

func TestScopedAdapterAzureAllowlistUsesSubscriptionOnly(t *testing.T) {
	scoped := newScopedAdapter(
		rowsAdapter{
			registryAdapter: registryAdapter{profile: "azure-prod", provider: model.ProviderAzure},
			page: Page{Rows: []map[string]any{
				{"id": "unauthorized", "scope": map[string]any{"subscription_id": "other-sub", "resource_group_id": "allowed-sub"}},
				{"id": "allowed", "scope": map[string]any{"subscription_id": "allowed-sub", "resource_group_id": "other-group"}},
			}},
		},
		config.Profile{Provider: model.ProviderAzure, Scopes: config.Scopes{Subscriptions: []string{"allowed-sub"}}},
	)

	page, err := scoped.Query(context.Background(), QueryRequest{Source: model.SourceResources})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(page.Rows) != 1 || page.Rows[0]["id"] != "allowed" {
		t.Fatalf("filtered rows = %#v, want only subscription-allowed row", page.Rows)
	}
	if _, err := scoped.Query(context.Background(), QueryRequest{Source: model.SourceResources, Accounts: []string{"other-sub"}}); err == nil || !containsSubstring(err.Error(), "scope_not_allowed") {
		t.Fatalf("unauthorized requested subscription error = %v, want scope_not_allowed", err)
	}
}

func TestScopedAdapterGCPDoesNotCrossMatchOrganizationAndProjectIDs(t *testing.T) {
	scoped := newScopedAdapter(
		rowsAdapter{
			registryAdapter: registryAdapter{profile: "gcp-prod", provider: model.ProviderGCP},
			page: Page{Rows: []map[string]any{
				{"id": "organization-valid", "scope": map[string]any{"organization_id": "org-1"}},
				{"id": "project-valid", "scope": map[string]any{"project_id": "project-1"}},
				{"id": "organization-as-project", "scope": map[string]any{"project_id": "org-1"}},
				{"id": "project-as-organization", "scope": map[string]any{"organization_id": "project-1"}},
			}},
		},
		config.Profile{Provider: model.ProviderGCP, Scopes: config.Scopes{Organizations: []string{"org-1"}, Projects: []string{"project-1"}}},
	)

	page, err := scoped.Query(context.Background(), QueryRequest{Source: model.SourceResources})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	var ids []string
	for _, row := range page.Rows {
		ids = append(ids, row["id"].(string))
	}
	if want := []string{"organization-valid", "project-valid"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("filtered rows = %#v, want only type-matched organization/project rows", ids)
	}
}

func TestScopedAdapterTargetRegionsRespectSourceAndProviderSemantics(t *testing.T) {
	tests := []struct {
		name      string
		adapter   Adapter
		source    model.Source
		requested []string
		want      []string
		wantCode  string
	}{
		{
			name: "gcp metrics are global",
			adapter: newScopedAdapter(
				registryAdapter{profile: "gcp-prod", provider: model.ProviderGCP},
				config.Profile{Provider: model.ProviderGCP, Regions: []string{"us-central1", "asia-east1"}},
			),
			source:    model.SourceMetrics,
			requested: nil,
			want:      []string{""},
		},
		{
			name: "gcp iam ignores region fanout",
			adapter: newScopedAdapter(
				registryAdapter{profile: "gcp-prod", provider: model.ProviderGCP},
				config.Profile{Provider: model.ProviderGCP, Regions: []string{"us-central1", "asia-east1"}},
			),
			source:    model.SourceIAM,
			requested: []string{"us-central1"},
			want:      []string{""},
		},
		{
			name: "gcp resources fan out configured regions",
			adapter: newScopedAdapter(
				registryAdapter{profile: "gcp-prod", provider: model.ProviderGCP},
				config.Profile{Provider: model.ProviderGCP, Regions: []string{"us-central1", "asia-east1"}},
			),
			source: model.SourceResources,
			want:   []string{"us-central1", "asia-east1"},
		},
		{
			name: "azure metrics require exact configured regions",
			adapter: newScopedAdapter(
				registryAdapter{profile: "azure-prod", provider: model.ProviderAzure},
				config.Profile{Provider: model.ProviderAzure, Regions: []string{"eastasia", "southeastasia"}},
			),
			source: model.SourceMetrics,
			want:   []string{"eastasia", "southeastasia"},
		},
		{
			name: "azure metrics reject wildcard-only configuration",
			adapter: newScopedAdapter(
				registryAdapter{profile: "azure-prod", provider: model.ProviderAzure},
				config.Profile{Provider: model.ProviderAzure, Regions: []string{"*"}},
			),
			source:   model.SourceMetrics,
			wantCode: "capability_unavailable",
		},
		{
			name: "costs are global",
			adapter: newScopedAdapter(
				registryAdapter{profile: "aws-prod", provider: model.ProviderAWS},
				config.Profile{Provider: model.ProviderAWS, Regions: []string{"us-east-1", "eu-west-1"}},
			),
			source: model.SourceCosts,
			want:   []string{""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver, ok := tt.adapter.(interface {
				TargetRegions(model.Source, []string) ([]string, error)
			})
			if !ok {
				t.Fatal("scoped adapter does not expose TargetRegions")
			}
			got, err := resolver.TargetRegions(tt.source, tt.requested)
			if tt.wantCode != "" {
				var providerErr *Error
				if !asProviderError(err, &providerErr) || providerErr.Code != tt.wantCode {
					t.Fatalf("TargetRegions() error = %#v, want Error{Code: %s}", err, tt.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("TargetRegions() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("TargetRegions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func asProviderError(err error, target **Error) bool {
	if err == nil {
		return false
	}
	value, ok := err.(*Error)
	if ok {
		*target = value
	}
	return ok
}

func containsSubstring(value, substring string) bool {
	return len(value) >= len(substring) && stringIndex(value, substring) >= 0
}

func stringIndex(value, substring string) int {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return i
		}
	}
	return -1
}
