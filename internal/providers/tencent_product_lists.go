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

type tencentProductListSpec struct{ service, version, action, list, id string }

var tencentProductListSpecs = map[string]tencentProductListSpec{
	"tencent.cvm.list_instances":   {"cvm", "2017-03-12", "DescribeInstances", "InstanceSet", "InstanceId"},
	"tencent.cdb.list_instances":   {"cdb", "2017-03-20", "DescribeDBInstances", "Items", "InstanceId"},
	"tencent.redis.list_instances": {"redis", "2018-04-12", "DescribeInstances", "InstanceSet", "InstanceId"},
	"tencent.tke.list_clusters":    {"tke", "2018-05-25", "DescribeClusters", "Clusters", "ClusterId"},
}

func tencentProductListOperations() []provider.Operation {
	ops := []provider.Operation{}
	for _, name := range []string{"tencent.cvm.list_instances", "tencent.cdb.list_instances", "tencent.redis.list_instances", "tencent.tke.list_clusters"} {
		s := tencentProductListSpecs[name]
		ops = append(ops, provider.Operation{Name: name, Provider: model.ProviderTencent, Service: s.service, Description: "List resources directly in one configured region without Resource Center; returns existing detail fields (CVM includes addresses); offset pagination is not a snapshot", Parameters: map[string]any{}})
	}
	return ops
}

type tencentProductListCursor struct {
	Profile, Account, Region, Operation string
	Offset, Total                       int
}

func decodeTencentProductListCursor(token string, scope tencentProductListCursor) (tencentProductListCursor, error) {
	if token == "" {
		return scope, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentProductListCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.Operation != scope.Operation || c.Offset <= 0 || c.Total <= c.Offset {
		return scope, &provider.Error{Code: "invalid_cursor", Operation: scope.Operation, Message: "Product list cursor does not match query scope or has an invalid offset"}
	}
	return c, nil
}
func (a *tencentAdapter) readProductList(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	if err := (provider.Operation{Name: req.Operation, Parameters: map[string]any{}}).ValidateParams(req.Params); err != nil {
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
	scope, err := decodeTencentProductListCursor(req.PageToken, tencentProductListCursor{Profile: a.name, Account: account, Region: region, Operation: req.Operation})
	if err != nil {
		return provider.Page{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	spec := tencentProductListSpecs[req.Operation]
	client, err := a.tencentClient(spec.service+".tencentcloudapi.com", region, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest(spec.service, spec.version, spec.action)
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"Offset": scope.Offset, "Limit": limit}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(req.Operation, err)
	}
	return a.tencentProductListPage(response.GetBody(), scope, limit)
}
func (a *tencentAdapter) tencentProductListPage(body []byte, scope tencentProductListCursor, limit int) (provider.Page, error) {
	bad := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: scope.Operation, Message: message}
	}
	var payload struct {
		Response *struct {
			TotalCount  *int
			InstanceSet json.RawMessage
			Items       json.RawMessage
			Clusters    json.RawMessage
			Error       *struct{ Code string }
		}
	}
	if json.Unmarshal(body, &payload) != nil || payload.Response == nil {
		return bad("invalid Product list response")
	}
	p := payload.Response
	if p.Error != nil {
		if p.Error.Code == "" {
			return bad("invalid Product list error")
		}
		return provider.Page{}, &provider.Error{Code: p.Error.Code, Operation: scope.Operation, Message: "Tencent Product list request failed"}
	}
	spec := tencentProductListSpecs[scope.Operation]
	raw := p.InstanceSet
	if spec.list == "Items" {
		raw = p.Items
	}
	if spec.list == "Clusters" {
		raw = p.Clusters
	}
	var resources []map[string]any
	if p.TotalCount == nil || *p.TotalCount < 0 || len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &resources) != nil {
		return bad("missing or invalid Product list or total")
	}
	total := *p.TotalCount
	if scope.Offset > 0 && total != scope.Total {
		return provider.Page{}, &provider.Error{Code: "inventory_changed", Operation: scope.Operation, Message: "Product total changed during pagination; restart the list"}
	}
	if len(resources) > limit || scope.Offset > total || len(resources) > total-scope.Offset || (len(resources) == 0 && scope.Offset < total) {
		return bad("Product list pagination is inconsistent with total")
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(resources), Requests: 1}
	seen := map[string]bool{}
	for _, resource := range resources {
		id, ok := resource[spec.id].(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return bad("missing or duplicate Product ID")
		}
		seen[id] = true
		row, err := a.tencentProductListRow(resource, spec, scope.Region, scope.Account)
		if err != nil {
			return bad("invalid typed product fields")
		}
		page.Rows = append(page.Rows, row)
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

func (a *tencentAdapter) tencentProductListRow(resource map[string]any, s tencentProductListSpec, region, account string) (map[string]any, error) {
	body, err := json.Marshal(map[string]any{"Response": map[string]any{s.list: []map[string]any{resource}}})
	if err != nil {
		return nil, err
	}
	switch s.service {
	case "cvm":
		var p tencentCVMDetailResponse
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return a.tencentCVMDetailRow(p.Response.InstanceSet[0], region, account), nil
	case "cdb":
		var p tencentCDBDetailResponse
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return a.tencentCDBDetailRow(p.Response.Items[0], region, account), nil
	case "tke":
		var p tencentTKEDetailResponse
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return a.tencentTKEDetailRow(p.Response.Clusters[0], region, account), nil
	default:
		var p struct {
			Response struct{ InstanceSet []tencentRedisDetail }
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return nil, err
		}
		return a.tencentRedisRow(p.Response.InstanceSet[0], region, account), nil
	}
}
