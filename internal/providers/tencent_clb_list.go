package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"strings"
)

const tencentCLBListOperationName = "tencent.clb.list_load_balancers"

func tencentCLBListOperation() provider.Operation {
	return provider.Operation{Name: tencentCLBListOperationName, Provider: model.ProviderTencent, Service: "clb", Description: "List CLB instances directly in one configured region without Resource Center; offset pagination is not a snapshot; excludes addresses, listeners and targets", Parameters: map[string]any{}}
}

type tencentCLBListCursor struct {
	Profile, Account, Region, Operation string
	Offset, Total                       int
}

func decodeTencentCLBListCursor(token string, scope tencentCLBListCursor) (tencentCLBListCursor, error) {
	if token == "" {
		return scope, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentCLBListCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.Operation != scope.Operation || c.Offset <= 0 || c.Total <= c.Offset {
		return scope, &provider.Error{Code: "invalid_cursor", Operation: tencentCLBListOperationName, Message: "CLB list cursor does not match query scope or has an invalid offset"}
	}
	return c, nil
}
func (a *tencentAdapter) readCLBList(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if err := tencentCLBListOperation().ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	scope, err := decodeTencentCLBListCursor(req.PageToken, tencentCLBListCursor{Profile: a.name, Account: account, Region: region, Operation: req.Operation})
	if err != nil {
		return provider.Page{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	client, err := a.tencentClient("clb.tencentcloudapi.com", region, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("clb", "2018-03-17", "DescribeLoadBalancers")
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"Offset": scope.Offset, "Limit": limit}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(req.Operation, err)
	}
	return a.tencentCLBListPage(response.GetBody(), scope, limit)
}
func (a *tencentAdapter) tencentCLBListPage(body []byte, scope tencentCLBListCursor, limit int) (provider.Page, error) {
	bad := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentCLBListOperationName, Message: message}
	}
	var payload struct {
		Response *struct {
			TotalCount      *int
			LoadBalancerSet json.RawMessage
			Error           *struct{ Code string }
		}
	}
	if json.Unmarshal(body, &payload) != nil || payload.Response == nil {
		return bad("invalid CLB list response")
	}
	p := payload.Response
	if p.Error != nil {
		if p.Error.Code == "" {
			return bad("invalid CLB list error")
		}
		return provider.Page{}, &provider.Error{Code: p.Error.Code, Operation: tencentCLBListOperationName, Message: "Tencent CLB list request failed"}
	}
	var resources []map[string]any
	if p.TotalCount == nil || *p.TotalCount < 0 || len(p.LoadBalancerSet) == 0 || string(p.LoadBalancerSet) == "null" || json.Unmarshal(p.LoadBalancerSet, &resources) != nil {
		return bad("missing or invalid CLB list or total")
	}
	total := *p.TotalCount
	if scope.Offset > 0 && total != scope.Total {
		return provider.Page{}, &provider.Error{Code: "inventory_changed", Operation: tencentCLBListOperationName, Message: "CLB total changed during pagination; restart the list"}
	}
	if len(resources) > limit || scope.Offset > total || len(resources) > total-scope.Offset || (len(resources) == 0 && scope.Offset < total) {
		return bad("CLB list pagination is inconsistent with total")
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(resources), Requests: 1}
	seen := map[string]bool{}
	for _, resource := range resources {
		id, ok := resource["LoadBalancerId"].(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return bad("missing or duplicate CLB ID")
		}
		seen[id] = true
		page.Rows = append(page.Rows, a.tencentCLBRow(resource, scope.Region, scope.Account))
	}
	next := scope.Offset + len(resources)
	if next < total {
		scope.Offset = next
		scope.Total = total
		data, _ := json.Marshal(scope)
		page.NextToken = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}
