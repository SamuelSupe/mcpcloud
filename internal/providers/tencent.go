package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	tcprofile "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	tencentResourcesOperation = "tencent.cloudrc.search_resources"
	tencentMetricsOperation   = "tencent.monitor.get_monitor_data"
	tencentCostsOperation     = "tencent.billing.describe_cost_explorer_summary"
)

type tencentAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderTencent, func(name string, p config.Profile) (provider.Adapter, error) {
		return &tencentAdapter{name: name, profile: p}, nil
	})
}
func (a *tencentAdapter) Provider() model.Provider { return model.ProviderTencent }
func (a *tencentAdapter) Profile() string          { return a.name }
func (a *tencentAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderTencent, tencentResourcesOperation, tencentMetricsOperation, tencentCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceResources && a.profile.Options["resource_mode"] == "direct" {
			c[i].Operations = append([]string{tencentDirectResourcesOperation}, tencentDirectResourceOperations...)
			c[i].Domains = []string{"compute", "database", "kubernetes", "network", "storage"}
			c[i].Kinds = []string{"instance", "database", "cache", "cluster", "load_balancer", "public_ip", "nat_gateway", "bucket"}
			c[i].Notes = "Direct product inventory: CVM, MySQL, Redis, TKE, CLB, EIP, NAT and COS only; one exact region and one configured account; CVM rows include addresses; no snapshot guarantee"
			if len(a.profile.Scopes.Accounts) != 1 || !hasExactRegion(a.profile.Regions) {
				c[i].Status = "not_configured"
			}
		}
		if (c[i].Source == model.SourceMetrics || c[i].Source == model.SourceCosts) && len(a.profile.Scopes.Accounts) != 1 {
			c[i].Status = "not_configured"
			c[i].Notes = "requires exactly one scopes.accounts entry because these APIs do not select or return a target account"
		}
		if c[i].Source == model.SourceMetrics && !hasExactRegion(a.profile.Regions) {
			c[i].Status = "not_configured"
			c[i].Notes = "requires at least one exact profile region and exactly one scopes.accounts entry"
		}
		if c[i].Source == model.SourceCosts && a.profile.Options["billing_currency"] == "" {
			c[i].Status = "not_configured"
			c[i].Notes = "requires options.billing_currency because the cost summary API does not return a currency; exactly one scopes.accounts entry is also required"
		}
	}
	return c
}
func (a *tencentAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(tencentResourcesOperation, model.ProviderTencent, "cloudrc", "Search Tencent Cloud Resource Center", map[string]any{"view_id": map[string]any{"type": "string"}, "resource_type": map[string]any{"type": "string"}, "resource_id": map[string]any{"type": "string"}, "resource_alias": map[string]any{"type": "string"}, "region": map[string]any{"type": "string"}, "zone": map[string]any{"type": "string"}, "vpc_id": map[string]any{"type": "string"}, "subnet_id": map[string]any{"type": "string"}})}
	operations = append(operations, nativeProductOperations(model.ProviderTencent, "cloudrc")...)
	operations = append(operations, instanceDetailOperations(model.ProviderTencent)...)
	operations = append(operations, tencentNodesOperation())
	operations = append(operations, tencentRedisOperation())
	operations = append(operations, tencentVPCOperations()...)
	operations = append(operations, tencentVPCListOperations()...)
	operations = append(operations, tencentProductListOperations()...)
	operations = append(operations, tencentCLBOperation(), tencentListenersOperation(), tencentCLBListOperation())
	operations = append(operations, tencentTargetOperations()...)
	operations = append(operations, tencentNATRuleOperations()...)
	operations = append(operations, tencentCOSConfigOperation(), tencentCOSListOp())
	return append(operations, deepDetailOperations(model.ProviderTencent)...)
}
func (a *tencentAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderTencent, a.profile, []string{"TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_KEY"})
}
func (a *tencentAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceResources && a.profile.Options["resource_mode"] == "direct" {
		return a.queryDirectResources(ctx, req)
	}
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	if req.Source != model.SourceResources && req.Source != model.SourceIAM {
		return provider.Page{}, unsupportedSource(req.Source, tencentResourcesOperation)
	}
	if _, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, tencentResourcesOperation, "account"); err != nil {
		return provider.Page{}, err
	}
	filters := map[string][]string{}
	if req.Region != "" && req.Region != "*" {
		filters["RegionCode"] = []string{req.Region}
	}
	page, err := a.search(ctx, a.profile.Options["resource_view_id"], filters, req.PageToken, req.Limit)
	if req.Source == model.SourceIAM {
		page = domainPage(page, "iam")
	}
	return page, err
}
func (a *tencentAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if _, ok := tencentProductListSpecs[req.Operation]; ok {
		return a.readProductList(ctx, req)
	}
	if req.Operation == tencentAddressListOperation || req.Operation == tencentNATListOperation {
		return a.readVPCList(ctx, req)
	}
	if req.Operation == tencentCLBListOperationName {
		return a.readCLBList(ctx, req)
	}
	if req.Operation == tencentCOSListOperation {
		return a.readCOSList(ctx, req)
	}
	if req.Operation == tencentCOSOperation {
		return a.readCOSConfig(ctx, req)
	}
	if req.Operation == tencentSNATOperation || req.Operation == tencentDNATOperation {
		return a.readNATRules(ctx, req)
	}
	if req.Operation == tencentTargetsOperationName || req.Operation == tencentTargetHealthOperationName {
		return a.readCLBTargets(ctx, req)
	}
	if req.Operation == tencentListenersOperationName {
		return a.readCLBListeners(ctx, req, tencentCLBSpec)
	}
	if req.Operation == tencentCLBOperationName {
		return a.readCLBDetail(ctx, req, tencentCLBSpec)
	}
	if spec, ok := tencentVPCSpecs[req.Operation]; ok {
		return a.readVPCDetail(ctx, req, spec)
	}
	if req.Operation == tencentRedisOperationName {
		return a.readRedisDetail(ctx, req)
	}
	if req.Operation == tencentNodesOperationName {
		return a.readClusterNodes(ctx, req)
	}
	if detail, ok := instanceDetailFor(model.ProviderTencent, req.Operation); ok {
		return a.describeCVMInstance(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderTencent, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderTencent, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderTencent, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		baseRequest.Operation = tencentResourcesOperation
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != tencentResourcesOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	view, err := nativeString(req.Params, "view_id")
	if err != nil {
		return provider.Page{}, err
	}
	configuredView := a.profile.Options["resource_view_id"]
	if configuredView != "" {
		if view != "" && view != configuredView {
			return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: tencentResourcesOperation, Message: "resource view is outside the profile allowlist"}
		}
		view = configuredView
	}
	filters := map[string][]string{}
	for param, key := range map[string]string{"resource_type": "ResourceType", "resource_id": "ResourceId", "resource_alias": "ResourceAlias", "region": "RegionCode", "zone": "ZoneCode", "vpc_id": "VpcId", "subnet_id": "SubnetId"} {
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

func (a *tencentAdapter) credential() (tencent.CredentialIface, error) {
	if a.profile.Credential.Source == "env" {
		id := envValue(a.profile, "TENCENTCLOUD_SECRET_ID", "TENCENTCLOUD_SECRET_ID")
		key := envValue(a.profile, "TENCENTCLOUD_SECRET_KEY", "TENCENTCLOUD_SECRET_KEY")
		token := envValue(a.profile, "TENCENTCLOUD_TOKEN", "TENCENTCLOUD_TOKEN")
		if id == "" || key == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: tencentResourcesOperation, Message: "Tencent Cloud credential environment variables are not set"}
		}
		if token != "" {
			return tencent.NewTokenCredential(id, key, token), nil
		}
		return tencent.NewCredential(id, key), nil
	}
	return tencent.DefaultProviderChain().GetCredential()
}

func (a *tencentAdapter) search(ctx context.Context, view string, filters map[string][]string, pageToken string, limit int) (provider.Page, error) {
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: tencentResourcesOperation, Message: err.Error()}
	}
	clientProfile := tcprofile.NewClientProfile()
	clientProfile.HttpProfile.Endpoint = "cloudrc.tencentcloudapi.com"
	clientProfile.NetworkFailureMaxRetries = 0
	clientProfile.RateLimitExceededMaxRetries = 0
	client := tencent.NewCommonClient(credential, "", clientProfile)
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	params := map[string]any{"MaxResults": limit}
	if view != "" {
		params["ViewId"] = view
	}
	if pageToken != "" {
		params["NextToken"] = pageToken
	}
	if len(filters) > 0 {
		items := make([]map[string]any, 0, len(filters))
		for key, values := range filters {
			items = append(items, map[string]any{"Key": key, "Values": values, "MatchType": "Equals"})
		}
		params["Filters"] = items
	}
	request := tchttp.NewCommonRequest("cloudrc", "2024-06-06", "SearchResources")
	request.SetContext(ctx)
	if err := request.SetActionParameters(params); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, &provider.Error{Code: "tencent_api_error", Operation: tencentResourcesOperation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "requestlimit", "timeout")}
	}
	var envelope struct {
		Response struct {
			NextToken string `json:"NextToken"`
			Resources []struct {
				ResourceID    string   `json:"ResourceId"`
				ResourceAlias string   `json:"ResourceAlias"`
				Uin           int64    `json:"Uin"`
				ResourceType  string   `json:"ResourceType"`
				RegionCode    string   `json:"RegionCode"`
				ZoneCode      string   `json:"ZoneCode"`
				PayMode       string   `json:"PayMode"`
				CreateTime    string   `json:"CreateTime"`
				ExpireTime    string   `json:"ExpireTime"`
				PrivateIP     []string `json:"PrivateIpAddress"`
				PublicIP      []string `json:"PublicIpAddress"`
				Tags          []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"Tags"`
			} `json:"Resources"`
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &envelope); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentResourcesOperation, Message: err.Error()}
	}
	if envelope.Response.Error != nil {
		code := strings.ToLower(envelope.Response.Error.Code)
		return provider.Page{}, &provider.Error{Code: envelope.Response.Error.Code, Operation: tencentResourcesOperation, Message: envelope.Response.Error.Message, Retryable: containsAny(code, "requestlimit", "throttl", "internalerror")}
	}
	rows := make([]map[string]any, 0, len(envelope.Response.Resources))
	now := time.Now().UTC()
	for _, resource := range envelope.Response.Resources {
		service := tencentService(resource.ResourceType)
		row := baseRow(model.ProviderTencent, a.name, resource.ResourceID, resource.ResourceAlias, service, resource.ResourceType, resource.RegionCode, fmt.Sprint(resource.Uin), now)
		row["zone"] = resource.ZoneCode
		tags := map[string]any{}
		for _, tag := range resource.Tags {
			tags[tag.Key] = tag.Value
		}
		row["tags"] = tags
		attrs := row["attributes"].(map[string]any)
		attrs["pay_mode"] = resource.PayMode
		attrs["private_ip_addresses"] = resource.PrivateIP
		attrs["public_ip_addresses"] = resource.PublicIP
		if parsed, err := time.Parse("2006-01-02 15:04:05", resource.CreateTime); err == nil {
			row["created_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		if parsed, err := time.Parse("2006-01-02 15:04:05", resource.ExpireTime); err == nil {
			attrs["expires_at"] = parsed.UTC().Format(time.RFC3339Nano)
		}
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: envelope.Response.NextToken, Scanned: len(rows), Requests: 1}, nil
}

func tencentService(resourceType string) string {
	parts := strings.Split(resourceType, "::")
	if len(parts) >= 2 && parts[1] != "" {
		return parts[1]
	}
	return "cloudrc"
}
