package providers

import (
	"context"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	aliresource "github.com/aliyun/alibaba-cloud-sdk-go/services/resourcecenter"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	alibabaResourcesOperation = "alibaba.resourcecenter.search_resources"
	alibabaMetricsOperation   = "alibaba.cms.describe_metric_list"
	alibabaCostsOperation     = "alibaba.bssopenapi.query_instance_bill"
)

type alibabaAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderAlibaba, func(name string, p config.Profile) (provider.Adapter, error) {
		return &alibabaAdapter{name: name, profile: p}, nil
	})
}
func (a *alibabaAdapter) Provider() model.Provider { return model.ProviderAlibaba }
func (a *alibabaAdapter) Profile() string          { return a.name }
func (a *alibabaAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderAlibaba, alibabaResourcesOperation, alibabaMetricsOperation, alibabaCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceMetrics && len(a.profile.Scopes.Accounts) != 1 {
			c[i].Status = "not_configured"
			c[i].Notes = "requires exactly one scopes.accounts entry because CloudMonitor does not select a target account"
		}
	}
	return c
}
func (a *alibabaAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(alibabaResourcesOperation, model.ProviderAlibaba, "resourcecenter", "Search Alibaba Cloud Resource Center", map[string]any{"view": map[string]any{"type": "string"}, "resource_type": map[string]any{"type": "string"}, "resource_id": map[string]any{"type": "string"}, "resource_name": map[string]any{"type": "string"}, "resource_group_id": map[string]any{"type": "string"}, "region": map[string]any{"type": "string"}})}
	operations = append(operations, nativeProductOperations(model.ProviderAlibaba, "resourcecenter")...)
	operations = append(operations, instanceDetailOperations(model.ProviderAlibaba)...)
	return append(operations, deepDetailOperations(model.ProviderAlibaba)...)
}
func (a *alibabaAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderAlibaba, a.profile, []string{"ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_SECRET"})
}
func (a *alibabaAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	if req.Source != model.SourceResources && req.Source != model.SourceIAM {
		return provider.Page{}, unsupportedSource(req.Source, alibabaResourcesOperation)
	}
	if _, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, alibabaResourcesOperation, "account"); err != nil {
		return provider.Page{}, err
	}
	filters := map[string][]string{}
	if req.Region != "" && req.Region != "*" {
		filters["RegionId"] = []string{req.Region}
	}
	page, err := a.search(ctx, a.profile.Options["resource_view"], filters, req.PageToken, req.Limit)
	if req.Source == model.SourceIAM {
		page = domainPage(page, "iam")
	}
	return page, err
}
func (a *alibabaAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderAlibaba, req.Operation); ok {
		return a.describeInstanceAttribute(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderAlibaba, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderAlibaba, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderAlibaba, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		baseRequest.Operation = alibabaResourcesOperation
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != alibabaResourcesOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	view, err := nativeString(req.Params, "view")
	if err != nil {
		return provider.Page{}, err
	}
	configuredView := a.profile.Options["resource_view"]
	if configuredView != "" {
		if view != "" && view != configuredView {
			return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: alibabaResourcesOperation, Message: "resource view is outside the profile allowlist"}
		}
		view = configuredView
	}
	filters := map[string][]string{}
	for param, key := range map[string]string{"resource_type": "ResourceType", "resource_id": "ResourceId", "resource_name": "ResourceName", "resource_group_id": "ResourceGroupId", "region": "RegionId"} {
		value, err := nativeString(req.Params, param)
		if err != nil {
			return provider.Page{}, err
		}
		if value != "" {
			filters[key] = []string{value}
		}
	}
	return a.search(ctx, view, filters, req.PageToken, req.Limit)
}

func (a *alibabaAdapter) client() (*aliresource.Client, error) {
	region := "cn-hangzhou"
	if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
		region = a.profile.Regions[0]
	}
	if a.profile.Credential.Source == "env" {
		access := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
		secret := envValue(a.profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
		token := envValue(a.profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
		if access == "" || secret == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: alibabaResourcesOperation, Message: "Alibaba Cloud credential environment variables are not set"}
		}
		if token != "" {
			return aliresource.NewClientWithStsToken(region, access, secret, token)
		}
		return aliresource.NewClientWithAccessKey(region, access, secret)
	}
	return aliresource.NewClient()
}
func (a *alibabaAdapter) search(ctx context.Context, view string, filters map[string][]string, pageToken string, limit int) (provider.Page, error) {
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	client, err := a.client()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: alibabaResourcesOperation, Message: err.Error()}
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	request := newAlibabaSearchResourcesRequest()
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return provider.Page{}, ctx.Err()
		}
		request.SetReadTimeout(remaining)
		connectTimeout := minDuration(10*time.Second, remaining)
		request.SetConnectTimeout(connectTimeout)
	}
	request.MaxResults = requests.NewInteger(limit)
	request.NextToken = pageToken
	request.View = view
	if len(filters) > 0 {
		items := make([]aliresource.SearchResourcesFilter, 0, len(filters))
		for key, values := range filters {
			values := append([]string(nil), values...)
			items = append(items, aliresource.SearchResourcesFilter{Key: key, MatchType: "Equals", Value: &values})
		}
		request.Filter = &items
	}
	response, err := client.SearchResources(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "alibaba_api_error", Operation: alibabaResourcesOperation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	rows := make([]map[string]any, 0, len(response.Resources))
	now := time.Now().UTC()
	for _, resource := range response.Resources {
		service := alibabaService(resource.ResourceType)
		row := baseRow(model.ProviderAlibaba, a.name, resource.ResourceId, resource.ResourceName, service, resource.ResourceType, resource.RegionId, resource.AccountId, now)
		row["zone"] = resource.ZoneId
		scope := row["scope"].(map[string]any)
		scope["resource_group_id"] = resource.ResourceGroupId
		tags := map[string]any{}
		for _, tag := range resource.Tags {
			tags[tag.Key] = tag.Value
		}
		row["tags"] = tags
		attrs := row["attributes"].(map[string]any)
		attrs["ip_addresses"] = resource.IpAddresses
		native := row["native"].(map[string]any)
		native["resource_type"] = resource.ResourceType
		if parsed, err := time.Parse(time.RFC3339, resource.CreateTime); err == nil {
			row["created_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		if parsed, err := time.Parse(time.RFC3339, resource.ExpireTime); err == nil {
			attrs["expires_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: response.NextToken, Scanned: len(rows), Requests: 1}, nil
}

func newAlibabaSearchResourcesRequest() *aliresource.SearchResourcesRequest {
	request := aliresource.CreateSearchResourcesRequest()
	// Resource Center is global. Avoid the SDK's invalid regional TLS endpoint.
	request.SetDomain("resourcecenter.aliyuncs.com")
	return request
}

func alibabaService(resourceType string) string {
	parts := strings.Split(resourceType, "::")
	if len(parts) >= 2 {
		return strings.ToLower(parts[1])
	}
	return "resourcecenter"
}
