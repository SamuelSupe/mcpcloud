package providers

import (
	"context"
	"strings"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	httpconfig "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/config"
	rms "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rms/v1"
	rmsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rms/v1/model"
	rmsregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rms/v1/region"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	huaweiResourcesOperation = "huawei.rms.list_all_resources"
	huaweiMetricsOperation   = "huawei.ces.batch_list_metric_data"
	huaweiCostsOperation     = "huawei.bss.list_costs"
)

type huaweiAdapter struct {
	name    string
	profile config.Profile
}

func init() {
	provider.RegisterFactory(model.ProviderHuawei, func(name string, p config.Profile) (provider.Adapter, error) {
		return &huaweiAdapter{name: name, profile: p}, nil
	})
}
func (a *huaweiAdapter) Provider() model.Provider { return model.ProviderHuawei }
func (a *huaweiAdapter) Profile() string          { return a.name }
func (a *huaweiAdapter) Capabilities() []provider.Capability {
	c := capabilities(model.ProviderHuawei, huaweiResourcesOperation, huaweiMetricsOperation, huaweiCostsOperation)
	for i := range c {
		if c[i].Source == model.SourceMetrics && len(a.profile.Scopes.Projects) == 0 {
			c[i].Status = "not_configured"
			c[i].Notes = "requires at least one scopes.projects entry for the project-scoped CES API"
		}
		if c[i].Source == model.SourceCosts && a.profile.Options["billing_site"] == "" {
			c[i].Status = "not_configured"
			c[i].Notes = "requires options.billing_site to select the china or international BSS endpoint"
		}
	}
	return c
}
func (a *huaweiAdapter) Operations() []provider.Operation {
	operations := []provider.Operation{operation(huaweiResourcesOperation, model.ProviderHuawei, "rms", "List Huawei Cloud resources through RMS", map[string]any{"region": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "enterprise_project_id": map[string]any{"type": "string"}})}
	operations = append(operations, nativeProductOperations(model.ProviderHuawei, "rms")...)
	operations = append(operations, instanceDetailOperations(model.ProviderHuawei)...)
	return append(operations, deepDetailOperations(model.ProviderHuawei)...)
}
func (a *huaweiAdapter) Readiness(context.Context) model.ProfileStatus {
	return readiness(a.name, model.ProviderHuawei, a.profile, []string{"HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_SK"})
}
func (a *huaweiAdapter) Query(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	if req.Source == model.SourceMetrics {
		return a.queryMetrics(ctx, req)
	}
	if req.Source == model.SourceCosts {
		return a.queryCosts(ctx, req)
	}
	if req.Source != model.SourceResources && req.Source != model.SourceIAM {
		return provider.Page{}, unsupportedSource(req.Source, huaweiResourcesOperation)
	}
	allowed := a.profile.Scopes.Projects
	if len(allowed) == 0 {
		allowed = a.profile.Scopes.Accounts
	}
	projects, err := restrictValues(allowed, req.Accounts, huaweiResourcesOperation, "project")
	if err != nil {
		return provider.Page{}, err
	}
	page, err := a.list(ctx, map[string]string{"region": req.Region}, projects, req.PageToken, req.Limit)
	if req.Source == model.SourceIAM {
		page = domainPage(page, "iam")
	}
	return page, err
}
func (a *huaweiAdapter) NativeRead(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if detail, ok := instanceDetailFor(model.ProviderHuawei, req.Operation); ok {
		return a.showServerDetail(ctx, req, detail)
	}
	if detail, ok := deepDetailFor(model.ProviderHuawei, req.Operation); ok {
		return a.readDeepDetail(ctx, req, detail)
	}
	product, productRead := nativeProductFor(model.ProviderHuawei, req.Operation)
	if productRead {
		if err := validateNativeProductRequest(model.ProviderHuawei, req.Operation, req.Params); err != nil {
			return provider.Page{}, err
		}
		baseRequest := req
		baseRequest.Operation = huaweiResourcesOperation
		page, err := a.NativeRead(ctx, baseRequest)
		return completeNativeProduct(page, err, product, req.Operation)
	}
	if req.Operation != huaweiResourcesOperation {
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	params := map[string]string{}
	for _, key := range []string{"region", "type", "id", "name", "enterprise_project_id"} {
		value, err := nativeString(req.Params, key)
		if err != nil {
			return provider.Page{}, err
		}
		if value != "" {
			params[key] = value
		}
	}
	allowed := a.profile.Scopes.Projects
	if len(allowed) == 0 {
		allowed = a.profile.Scopes.Accounts
	}
	return a.list(ctx, params, allowed, req.PageToken, req.Limit)
}

func (a *huaweiAdapter) list(ctx context.Context, params map[string]string, allowedProjects []string, pageToken string, limit int) (provider.Page, error) {
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	regionID := params["region"]
	if regionID == "" || regionID == "*" {
		if len(a.profile.Regions) > 0 && a.profile.Regions[0] != "*" {
			regionID = a.profile.Regions[0]
		} else {
			regionID = "cn-north-4"
		}
	}
	ak := envValue(a.profile, "HUAWEICLOUD_SDK_AK", "HUAWEICLOUD_SDK_AK")
	sk := envValue(a.profile, "HUAWEICLOUD_SDK_SK", "HUAWEICLOUD_SDK_SK")
	token := envValue(a.profile, "HUAWEICLOUD_SDK_SECURITY_TOKEN", "HUAWEICLOUD_SDK_SECURITY_TOKEN")
	if ak == "" || sk == "" {
		return provider.Page{}, &provider.Error{Code: "missing_credentials", Operation: huaweiResourcesOperation, Message: "Huawei Cloud credential environment variables are not set"}
	}
	credential := basic.NewCredentialsBuilder().WithAk(ak).WithSk(sk).WithSecurityToken(token).Build()
	region, err := rmsregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: huaweiResourcesOperation, Message: err.Error()}
	}
	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
		if timeout <= 0 {
			return provider.Page{}, ctx.Err()
		}
	}
	hc, err := rms.RmsClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(httpconfig.DefaultHttpConfig().WithTimeout(timeout).WithRetries(0)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiResourcesOperation, Message: err.Error()}
	}
	client := rms.NewRmsClient(hc)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	limit32 := int32(limit)
	request := &rmsmodel.ListAllResourcesRequest{RegionId: &regionID, Limit: &limit32}
	if pageToken != "" {
		request.Marker = &pageToken
	}
	if value := params["type"]; value != "" {
		request.Type = &value
	}
	if value := params["id"]; value != "" {
		request.Id = &value
	}
	if value := params["name"]; value != "" {
		request.Name = &value
	}
	if value := params["enterprise_project_id"]; value != "" {
		request.EpId = &value
	}
	response, err := client.ListAllResources(request)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "huawei_api_error", Operation: huaweiResourcesOperation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	var resources []rmsmodel.ResourceEntity
	if response.Resources != nil {
		resources = *response.Resources
	}
	rows := make([]map[string]any, 0, len(resources))
	now := time.Now().UTC()
	for _, resource := range resources {
		row := a.row(resource, now)
		if len(allowedProjects) > 0 {
			scope := row["scope"].(map[string]any)
			project := stringValue(scope["project_id"])
			allowed := false
			for _, candidate := range allowedProjects {
				if project == candidate {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		rows = append(rows, row)
	}
	next := ""
	if response.PageInfo != nil && response.PageInfo.NextMarker != nil {
		next = *response.PageInfo.NextMarker
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(resources), Requests: 1}, nil
}

func (a *huaweiAdapter) row(resource rmsmodel.ResourceEntity, observed time.Time) map[string]any {
	id, name, service, nativeType, region, project := "", "", "", "", "", ""
	if resource.Id != nil {
		id = *resource.Id
	}
	if resource.Name != nil {
		name = *resource.Name
	}
	if resource.Provider != nil {
		service = *resource.Provider
	}
	if resource.Type != nil {
		nativeType = service + "." + *resource.Type
	}
	if resource.RegionId != nil {
		region = *resource.RegionId
	}
	if resource.ProjectId != nil {
		project = *resource.ProjectId
	}
	row := baseRow(model.ProviderHuawei, a.name, id, name, service, nativeType, region, "", observed)
	scope := row["scope"].(map[string]any)
	scope["project_id"] = project
	scope["resource_group_id"] = deref(resource.EpId)
	row["state"] = deref(resource.ProvisioningState)
	tags := map[string]any{}
	for key, value := range resource.Tags {
		tags[key] = value
	}
	row["tags"] = tags
	attrs := row["attributes"].(map[string]any)
	attrs["project_name"] = deref(resource.ProjectName)
	attrs["enterprise_project_name"] = deref(resource.EpName)
	for key, value := range map[string]*string{"created_at": resource.Created, "updated_at": resource.Updated} {
		if value == nil {
			continue
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, *value); err == nil {
				row[key] = parsed.UTC().Format(time.RFC3339Nano)
				break
			}
		}
	}
	native := row["native"].(map[string]any)
	native["provider_type"] = nativeType
	return row
}
