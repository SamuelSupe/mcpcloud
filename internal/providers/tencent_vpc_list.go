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

const tencentAddressListOperation = "tencent.vpc.list_addresses"
const tencentNATListOperation = "tencent.vpc.list_nat_gateways"

func tencentVPCListSpec(operation string) tencentVPCSpec {
	if operation == tencentAddressListOperation {
		return tencentVPCSpecs["tencent.vpc.describe_address"]
	}
	return tencentVPCSpecs["tencent.vpc.describe_nat_gateway"]
}
func tencentVPCListOperations() []provider.Operation {
	ops := []provider.Operation{}
	for _, name := range []string{tencentAddressListOperation, tencentNATListOperation} {
		ops = append(ops, provider.Operation{Name: name, Provider: model.ProviderTencent, Service: "vpc", Description: "List EIP or NAT instances directly in one configured region without Resource Center; offset pagination is not a snapshot; excludes addresses and embedded NAT rules", Parameters: map[string]any{}})
	}
	return ops
}

type tencentVPCListCursor struct {
	Profile, Account, Region, Operation string
	Offset, Total                       int
}

func decodeTencentVPCListCursor(token string, scope tencentVPCListCursor) (tencentVPCListCursor, error) {
	if token == "" {
		return scope, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentVPCListCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.Operation != scope.Operation || c.Offset <= 0 || c.Total <= c.Offset {
		return scope, &provider.Error{Code: "invalid_cursor", Operation: scope.Operation, Message: "VPC list cursor does not match query scope or has an invalid offset"}
	}
	return c, nil
}
func (a *tencentAdapter) readVPCList(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
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
	scope, err := decodeTencentVPCListCursor(req.PageToken, tencentVPCListCursor{Profile: a.name, Account: account, Region: region, Operation: req.Operation})
	if err != nil {
		return provider.Page{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	client, err := a.tencentClient("vpc.tencentcloudapi.com", region, req.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("vpc", "2017-03-12", tencentVPCListSpec(req.Operation).action)
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"Offset": scope.Offset, "Limit": limit}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(req.Operation, err)
	}
	return a.tencentVPCListPage(response.GetBody(), scope, limit)
}
func (a *tencentAdapter) tencentVPCListPage(body []byte, scope tencentVPCListCursor, limit int) (provider.Page, error) {
	bad := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: scope.Operation, Message: message}
	}
	var payload struct {
		Response *struct {
			TotalCount    *int
			AddressSet    json.RawMessage
			NatGatewaySet json.RawMessage
			Error         *struct{ Code string }
		}
	}
	if json.Unmarshal(body, &payload) != nil || payload.Response == nil {
		return bad("invalid VPC list response")
	}
	p := payload.Response
	if p.Error != nil {
		if p.Error.Code == "" {
			return bad("invalid VPC list error")
		}
		return provider.Page{}, &provider.Error{Code: p.Error.Code, Operation: scope.Operation, Message: "Tencent VPC list request failed"}
	}
	spec := tencentVPCListSpec(scope.Operation)
	raw := p.AddressSet
	if scope.Operation == tencentNATListOperation {
		raw = p.NatGatewaySet
	}
	var resources []map[string]any
	if p.TotalCount == nil || *p.TotalCount < 0 || len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &resources) != nil {
		return bad("missing or invalid VPC list or total")
	}
	total := *p.TotalCount
	if scope.Offset > 0 && total != scope.Total {
		return provider.Page{}, &provider.Error{Code: "inventory_changed", Operation: scope.Operation, Message: "VPC total changed during pagination; restart the list"}
	}
	if len(resources) > limit || scope.Offset > total || len(resources) > total-scope.Offset || (len(resources) == 0 && scope.Offset < total) {
		return bad("VPC list pagination is inconsistent with total")
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(resources), Requests: 1}
	seen := map[string]bool{}
	for _, resource := range resources {
		id, ok := resource[spec.id].(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return bad("missing or duplicate VPC ID")
		}
		seen[id] = true
		page.Rows = append(page.Rows, a.tencentVPCRow(resource, spec, scope.Region, scope.Account))
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
