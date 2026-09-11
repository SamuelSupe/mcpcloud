package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const tencentDirectResourcesOperation = "tencent.inventory.list_resources"

var tencentDirectResourceOperations = []string{"tencent.cvm.list_instances", "tencent.cdb.list_instances", "tencent.redis.list_instances", "tencent.tke.list_clusters", tencentCLBListOperationName, tencentAddressListOperation, tencentNATListOperation, tencentCOSListOperation}

type tencentInventoryCursor struct {
	Version                  int
	Profile, Account, Region string
	Product                  int
	Token                    string
}

func (a *tencentAdapter) queryDirectResources(ctx context.Context, req provider.QueryRequest) (provider.Page, error) {
	return a.queryDirectResourcesWithReader(ctx, req, a.NativeRead)
}
func (a *tencentAdapter) queryDirectResourcesWithReader(ctx context.Context, req provider.QueryRequest, read func(context.Context, provider.NativeRequest) (provider.Page, error)) (provider.Page, error) {
	if req.Source != model.SourceResources {
		return provider.Page{}, unsupportedSource(req.Source, tencentDirectResourcesOperation)
	}
	if a.profile.Options["resource_view_id"] != "" {
		return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: tencentDirectResourcesOperation, Message: "direct resources cannot use a resource-center view"}
	}
	if _, err := restrictValues(a.profile.Scopes.Accounts, req.Accounts, tencentDirectResourcesOperation, "account"); err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, tencentDirectResourcesOperation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, tencentDirectResourcesOperation)
	if err != nil {
		return provider.Page{}, err
	}
	scope := tencentInventoryCursor{Version: 1, Profile: a.name, Account: account, Region: region}
	if req.PageToken != "" {
		var c tencentInventoryCursor
		raw, err := base64.RawURLEncoding.DecodeString(req.PageToken)
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.Product < 0 || c.Product >= len(tencentDirectResourceOperations) || (c.Product == 0 && c.Token == "") {
			return provider.Page{}, &provider.Error{Code: "invalid_cursor", Operation: tencentDirectResourcesOperation, Message: "direct resource cursor does not match query scope"}
		}
		scope = c
	}
	// One product API page per adapter call preserves engine request/scan budgets.
	page, err := read(ctx, provider.NativeRequest{Operation: tencentDirectResourceOperations[scope.Product], Region: region, Params: map[string]any{}, PageToken: scope.Token, Limit: req.Limit})
	if err != nil {
		return provider.Page{}, err
	}
	if page.NextToken != "" {
		scope.Token = page.NextToken
	} else {
		scope.Product++
		scope.Token = ""
	}
	page.NextToken = ""
	if scope.Product < len(tencentDirectResourceOperations) {
		raw, _ := json.Marshal(scope)
		page.NextToken = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
