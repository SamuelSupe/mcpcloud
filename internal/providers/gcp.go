package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	gcpResourcesOperation = "gcp.cloudasset.search_all_resources"
	gcpIAMOperation       = "gcp.cloudasset.search_all_iam_policies"
	gcpMetricsOperation   = "gcp.monitoring.time_series.list"
	gcpCostsOperation     = "gcp.bigquery.billing_export.query"
)

var gcpScopePattern = regexp.MustCompile(`^(projects|folders|organizations)/[A-Za-z0-9._:-]+$`)
var gcpIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*$`)

func validGCPIdentifier(value string) bool {
	return len(value) <= 1024 && gcpIdentifierPattern.MatchString(value)
}

type gcpAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderGCP, func(name string, p config.Profile) (provider.Adapter, error) {
		if configured := p.Options["asset_scope"]; configured != "" && !gcpScopePattern.MatchString(configured) {
			return nil, fmt.Errorf("options.asset_scope is invalid")
		}
		if p.Options["asset_scope"] == "" && len(p.Scopes.Organizations) == 0 && len(p.Scopes.Projects) == 0 {
			return nil, fmt.Errorf("an asset_scope, organization, or project is required")
		}
		if table := p.Options["billing_table"]; table != "" {
			parts := strings.Split(table, ".")
			if len(parts) != 3 || !validGCPIdentifier(parts[0]) || !validGCPIdentifier(parts[1]) || !validGCPIdentifier(parts[2]) {
				return nil, fmt.Errorf("options.billing_table must be project.dataset.table using valid identifiers")
			}
		}
		for _, key := range []string{"billing_project", "billing_location"} {
			if value := p.Options[key]; value != "" && !validGCPIdentifier(value) {
				return nil, fmt.Errorf("options.%s is invalid", key)
			}
		}
		return &gcpAdapter{name: name, profile: p}, nil
	})
}

func (a *gcpAdapter) Provider() model.Provider { return model.ProviderGCP }
func (a *gcpAdapter) Profile() string          { return a.name }
func (a *gcpAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderGCP, gcpResourcesOperation, gcpMetricsOperation, gcpCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceIAM {
			c[i].Operations = append([]string{gcpIAMOperation}, nativeProductOperationNames(model.ProviderGCP, model.SourceIAM)...)
			c[i].Status = "available"
		}
		if c[i].Source == model.SourceMetrics && !a.hasMonitoringProject() {
			c[i].Status = "not_configured"
			c[i].Notes = "requires a project in scopes.projects or a project-level options.asset_scope"
		}
		if c[i].Source == model.SourceCosts && a.profile.Options["billing_table"] == "" {
			c[i].Status = "not_configured"
			c[i].Notes = "requires options.billing_table pointing to a preconfigured standard Billing Export table"
		}
	}
	return c
}
func (a *gcpAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{
		operation(gcpResourcesOperation, model.ProviderGCP, "cloudasset", "List resources in an allowed project, folder, or organization", map[string]any{"scope": map[string]any{"type": "string"}}),
		operation(gcpIAMOperation, model.ProviderGCP, "cloudasset", "List IAM policies in an allowed project, folder, or organization", map[string]any{"scope": map[string]any{"type": "string"}}),
	}
	operations = append(operations, nativeProductOperations(model.ProviderGCP, "cloudasset")...)
	operations = append(operations, instanceDetailOperations(model.ProviderGCP)...)
	return append(operations, deepDetailOperations(model.ProviderGCP)...)
}
func (a *gcpAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderGCP, a.profile, []string{"GOOGLE_APPLICATION_CREDENTIALS"})
}

func (a *gcpAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Region != "" && req.Region != "*" && !cloudRegionPattern.MatchString(req.Region) {
		return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: gcpResourcesOperation, Message: "GCP region is not a valid region identifier"}
	}
	scopeOverride := ""
	if len(req.Accounts) > 1 {
		return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: gcpResourcesOperation, Message: "GCP queries accept one project, folder, or organization scope per target"}
	}
	if len(req.Accounts) == 1 {
		scopeOverride = req.Accounts[0]
		if !strings.Contains(scopeOverride, "/") {
			scopeOverride = "projects/" + scopeOverride
		}
	}
	scope, err := a.scope(scopeOverride)
	if err != nil {
		return provider.Page{}, err
	}
	switch req.Source {
	case model.SourceResources:
		return a.searchResources(ctx, scope, req.Region, req.PageToken, req.Limit)
	case model.SourceIAM:
		return a.searchIAM(ctx, scope, req.PageToken, req.Limit)
	case model.SourceMetrics:
		if !strings.HasPrefix(scope, "projects/") {
			return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: gcpMetricsOperation, Message: "GCP Monitoring requires a project scope"}
		}
		return a.queryMetrics(ctx, scope, req)
	case model.SourceCosts:
		if len(req.Accounts) > 0 && !strings.HasPrefix(scope, "projects/") {
			return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: gcpCostsOperation, Message: "GCP cost account scopes must identify projects"}
		}
		return a.queryCosts(ctx, scope, req)
	default:
		return provider.Page{}, unsupportedSource(req.Source, gcpResourcesOperation)
	}
}

func (a *gcpAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderGCP, req.Operation); ok {
		return a.getInstanceDetail(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderGCP, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderGCP, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderGCP, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		if product.source == model.SourceIAM {
			baseRequest.Operation = gcpIAMOperation
		} else {
			baseRequest.Operation = gcpResourcesOperation
		}
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != gcpResourcesOperation && req.Operation != gcpIAMOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	scopeParam, err := nativeString(req.Params, "scope")
	if err != nil {
		return provider.Page{}, err
	}
	scope, err := a.scope(scopeParam)
	if err != nil {
		return provider.Page{}, err
	}
	switch req.Operation {
	case gcpResourcesOperation:
		return a.searchResources(ctx, scope, req.Region, req.PageToken, req.Limit)
	case gcpIAMOperation:
		return a.searchIAM(ctx, scope, req.PageToken, req.Limit)
	default:
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
}

func (a *gcpAdapter) hasMonitoringProject() bool {
	if strings.HasPrefix(a.profile.Options["asset_scope"], "projects/") {
		return true
	}
	return len(a.profile.Scopes.Projects) > 0
}

func (a *gcpAdapter) scope(override string) (string, error) {
	allowed := a.configuredScopes()
	if override != "" {
		if !gcpScopePattern.MatchString(override) {
			return "", fmt.Errorf("invalid GCP asset scope")
		}
		for _, candidate := range allowed {
			if override == candidate {
				return override, nil
			}
		}
		return "", &provider.Error{Code: "scope_not_allowed", Operation: gcpResourcesOperation, Message: "GCP asset scope is outside the profile allowlist"}
	}
	if len(allowed) > 0 {
		return allowed[0], nil
	}
	return "", &provider.Error{Code: "missing_scope", Operation: gcpResourcesOperation, Message: "GCP profile requires options.asset_scope, an organization, or a project"}
}

func (a *gcpAdapter) configuredScopes() []string {
	var scopes []string
	if configured := a.profile.Options["asset_scope"]; configured != "" && gcpScopePattern.MatchString(configured) {
		scopes = append(scopes, configured)
	}
	for _, organization := range a.profile.Scopes.Organizations {
		scopes = append(scopes, "organizations/"+organization)
	}
	for _, project := range a.profile.Scopes.Projects {
		scopes = append(scopes, "projects/"+project)
	}
	return scopes
}

func (a *gcpAdapter) httpClient(ctx context.Context) (*http.Client, error) {
	scopes := []string{"https://www.googleapis.com/auth/cloud-platform.read-only"}
	var credentials *google.Credentials
	var err error
	if a.profile.Credential.Source == "env" {
		path := envValue(a.profile, "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_APPLICATION_CREDENTIALS")
		if path == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: gcpResourcesOperation, Message: "GOOGLE_APPLICATION_CREDENTIALS is not set"}
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, &provider.Error{Code: "authentication_error", Operation: gcpResourcesOperation, Message: "credential file referenced by the environment could not be read"}
		}
		credentials, err = google.CredentialsFromJSON(ctx, data, scopes...)
	} else {
		credentials, err = google.FindDefaultCredentialsWithParams(ctx, google.CredentialsParams{Scopes: scopes})
	}
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: gcpResourcesOperation, Message: err.Error()}
	}
	return oauth2.NewClient(ctx, credentials.TokenSource), nil
}

type gcpResourceResponse struct {
	Results []struct {
		Name         string            `json:"name"`
		AssetType    string            `json:"assetType"`
		Project      string            `json:"project"`
		Folders      []string          `json:"folders"`
		Organization string            `json:"organization"`
		DisplayName  string            `json:"displayName"`
		Description  string            `json:"description"`
		Location     string            `json:"location"`
		Labels       map[string]string `json:"labels"`
		CreateTime   string            `json:"createTime"`
		UpdateTime   string            `json:"updateTime"`
		State        string            `json:"state"`
	} `json:"results"`
	NextPageToken string `json:"nextPageToken"`
}
type gcpIAMResponse struct {
	Results []struct {
		Resource     string   `json:"resource"`
		AssetType    string   `json:"assetType"`
		Project      string   `json:"project"`
		Folders      []string `json:"folders"`
		Organization string   `json:"organization"`
		Policy       struct {
			Bindings []struct {
				Role      string         `json:"role"`
				Members   []string       `json:"members"`
				Condition map[string]any `json:"condition"`
			} `json:"bindings"`
		} `json:"policy"`
	} `json:"results"`
	NextPageToken string `json:"nextPageToken"`
}

func (a *gcpAdapter) searchResources(ctx context.Context, scope, region, pageToken string, limit int) (provider.Page, error) {
	var response gcpResourceResponse
	query := ""
	if region != "" && region != "*" {
		query = "location:" + region
	}
	if err := a.get(ctx, scope, "searchAllResources", query, pageToken, limit, &response); err != nil {
		return provider.Page{}, err
	}
	rows := make([]map[string]any, 0, len(response.Results))
	now := time.Now().UTC()
	for _, resource := range response.Results {
		name := resource.DisplayName
		if name == "" {
			name = lastName(resource.Name)
		}
		service := gcpService(resource.AssetType)
		row := baseRow(model.ProviderGCP, a.name, resource.Name, name, service, resource.AssetType, resource.Location, strings.TrimPrefix(resource.Project, "projects/"), now)
		row["state"] = resource.State
		tags := map[string]any{}
		for k, v := range resource.Labels {
			tags[k] = v
		}
		row["tags"] = tags
		scopeMap := row["scope"].(map[string]any)
		scopeMap["project_id"] = strings.TrimPrefix(resource.Project, "projects/")
		scopeMap["organization_id"] = strings.TrimPrefix(resource.Organization, "organizations/")
		if parsed, err := time.Parse(time.RFC3339, resource.CreateTime); err == nil {
			row["created_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		if parsed, err := time.Parse(time.RFC3339, resource.UpdateTime); err == nil {
			row["updated_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		native := row["native"].(map[string]any)
		native["asset_type"] = resource.AssetType
		native["description"] = resource.Description
		native["folders"] = resource.Folders
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: response.NextPageToken, Scanned: len(rows), Requests: 1}, nil
}

func (a *gcpAdapter) searchIAM(ctx context.Context, scope, pageToken string, limit int) (provider.Page, error) {
	var response gcpIAMResponse
	if err := a.get(ctx, scope, "searchAllIamPolicies", "", pageToken, limit, &response); err != nil {
		return provider.Page{}, err
	}
	now := time.Now().UTC()
	var rows []map[string]any
	for _, result := range response.Results {
		for _, binding := range result.Policy.Bindings {
			for _, member := range binding.Members {
				row := baseRow(model.ProviderGCP, a.name, result.Resource, lastName(result.Resource), "iam", result.AssetType, "", strings.TrimPrefix(result.Project, "projects/"), now)
				row["domain"] = "iam"
				row["kind"] = "binding"
				row["id"] = result.Resource + "#" + binding.Role + "#" + member
				row["resource_id"] = result.Resource
				row["principal"] = member
				row["principal_type"] = strings.SplitN(member, ":", 2)[0]
				row["roles"] = []string{binding.Role}
				row["condition"] = binding.Condition
				scopeMap := row["scope"].(map[string]any)
				scopeMap["project_id"] = strings.TrimPrefix(result.Project, "projects/")
				scopeMap["organization_id"] = strings.TrimPrefix(result.Organization, "organizations/")
				native := row["native"].(map[string]any)
				native["asset_type"] = result.AssetType
				native["folders"] = result.Folders
				rows = append(rows, row)
			}
		}
	}
	return provider.Page{Rows: rows, NextToken: response.NextPageToken, Scanned: len(rows), Requests: 1}, nil
}

func (a *gcpAdapter) get(ctx context.Context, scope, method, query, pageToken string, limit int, target any) error {
	client, err := a.httpClient(ctx)
	if err != nil {
		return err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	endpoint := "https://cloudasset.googleapis.com/v1/" + scope + ":" + method
	values := url.Values{"pageSize": []string{fmt.Sprint(limit)}}
	if query != "" {
		values.Set("query", query)
	}
	if pageToken != "" {
		values.Set("pageToken", pageToken)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return &provider.Error{Code: "gcp_api_error", Operation: "gcp.cloudasset." + method, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return &provider.Error{Code: "gcp_api_error", Operation: "gcp.cloudasset." + method, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(target); err != nil {
		return &provider.Error{Code: "invalid_provider_response", Operation: "gcp.cloudasset." + method, Message: err.Error()}
	}
	return nil
}

func gcpService(assetType string) string {
	parts := strings.Split(assetType, "/")
	if len(parts) > 0 {
		return strings.TrimSuffix(parts[0], ".googleapis.com")
	}
	return "cloudasset"
}
