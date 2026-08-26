package model

import "time"

type Provider string

const (
	ProviderAWS        Provider = "aws"
	ProviderGCP        Provider = "gcp"
	ProviderAzure      Provider = "azure"
	ProviderAlibaba    Provider = "alibaba"
	ProviderHuawei     Provider = "huawei"
	ProviderTencent    Provider = "tencent"
	ProviderVolcengine Provider = "volcengine"
)

var Providers = []Provider{
	ProviderAWS,
	ProviderGCP,
	ProviderAzure,
	ProviderAlibaba,
	ProviderHuawei,
	ProviderTencent,
	ProviderVolcengine,
}

type Source string

const (
	SourceResources Source = "resources"
	SourceIAM       Source = "iam"
	SourceMetrics   Source = "metrics"
	SourceCosts     Source = "costs"
)

var Sources = []Source{SourceResources, SourceIAM, SourceMetrics, SourceCosts}

var NativeFields = map[Provider][]string{
	ProviderAWS:        {"resource_type", "arn", "cfn_resource_type", "instance_type", "namespace", "metric_name", "statistic", "metric"},
	ProviderGCP:        {"resource_type", "asset_type", "description", "folders", "instance_type", "metric_type", "billing_table"},
	ProviderAzure:      {"resource_type", "type", "resource_group", "instance_type", "metric_namespace", "metric_name", "statistic", "scope_type"},
	ProviderAlibaba:    {"resource_type", "instance_type", "namespace", "metric_name", "statistic", "product_code", "billing_item"},
	ProviderHuawei:     {"resource_type", "provider_type", "instance_type", "namespace", "metric_name", "statistic", "cost_type", "amount_type"},
	ProviderTencent:    {"resource_type", "instance_type", "namespace", "metric_name", "statistic", "dimension", "fee_type"},
	ProviderVolcengine: {"resource_type", "instance_type", "namespace", "sub_namespace", "metric_name", "statistic", "bill_category", "billing_mode"},
}

func NativeFieldAllowed(field string) bool {
	for _, cloud := range Providers {
		for _, candidate := range NativeFields[cloud] {
			if field == candidate {
				return true
			}
		}
	}
	return false
}

type Scope struct {
	OrganizationID  string `json:"organization_id,omitempty"`
	TenantID        string `json:"tenant_id,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	SubscriptionID  string `json:"subscription_id,omitempty"`
	ResourceGroupID string `json:"resource_group_id,omitempty"`
}

type Record struct {
	Provider   Provider       `json:"provider"`
	Profile    string         `json:"profile"`
	Domain     string         `json:"domain"`
	Service    string         `json:"service"`
	Kind       string         `json:"kind"`
	ID         string         `json:"id"`
	Name       string         `json:"name,omitempty"`
	Scope      Scope          `json:"scope"`
	Region     string         `json:"region,omitempty"`
	Zone       string         `json:"zone,omitempty"`
	State      string         `json:"state,omitempty"`
	Tags       map[string]any `json:"tags,omitempty"`
	CreatedAt  *time.Time     `json:"created_at,omitempty"`
	UpdatedAt  *time.Time     `json:"updated_at,omitempty"`
	ObservedAt time.Time      `json:"observed_at"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Native     map[string]any `json:"native,omitempty"`
}

func (r Record) Row(includeNative bool) map[string]any {
	row := map[string]any{
		"provider":    string(r.Provider),
		"profile":     r.Profile,
		"domain":      r.Domain,
		"service":     r.Service,
		"kind":        r.Kind,
		"id":          r.ID,
		"name":        r.Name,
		"scope":       structToMap(r.Scope),
		"region":      r.Region,
		"zone":        r.Zone,
		"state":       r.State,
		"tags":        r.Tags,
		"observed_at": r.ObservedAt.UTC().Format(time.RFC3339Nano),
		"attributes":  r.Attributes,
	}
	if r.CreatedAt != nil {
		row["created_at"] = r.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if r.UpdatedAt != nil {
		row["updated_at"] = r.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if includeNative {
		row["native"] = r.Native
	}
	return row
}

func structToMap(s Scope) map[string]any {
	return map[string]any{
		"organization_id":   s.OrganizationID,
		"tenant_id":         s.TenantID,
		"account_id":        s.AccountID,
		"project_id":        s.ProjectID,
		"subscription_id":   s.SubscriptionID,
		"resource_group_id": s.ResourceGroupID,
	}
}

type QueryError struct {
	Provider  Provider `json:"provider,omitempty"`
	Profile   string   `json:"profile,omitempty"`
	Scope     string   `json:"scope,omitempty"`
	Region    string   `json:"region,omitempty"`
	Operation string   `json:"operation,omitempty"`
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Retryable bool     `json:"retryable"`
}

type Coverage struct {
	Planned   int `json:"planned"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

type QueryStats struct {
	Requests   int       `json:"requests"`
	Scanned    int       `json:"scanned"`
	Returned   int       `json:"returned"`
	Truncated  bool      `json:"truncated"`
	DurationMS int64     `json:"duration_ms"`
	ObservedAt time.Time `json:"observed_at"`
}

type QueryResult struct {
	QueryID    string           `json:"query_id"`
	Status     string           `json:"status"`
	Columns    []string         `json:"columns"`
	Rows       []map[string]any `json:"rows"`
	NextCursor string           `json:"next_cursor,omitempty"`
	Coverage   Coverage         `json:"coverage"`
	Errors     []QueryError     `json:"errors,omitempty"`
	Stats      QueryStats       `json:"stats"`
}

type ProfileStatus struct {
	Name     string         `json:"name"`
	Provider Provider       `json:"provider"`
	Ready    bool           `json:"ready"`
	Status   string         `json:"status"`
	Scopes   map[string]any `json:"scopes,omitempty"`
	Regions  []string       `json:"regions,omitempty"`
	Error    string         `json:"error,omitempty"`
}
