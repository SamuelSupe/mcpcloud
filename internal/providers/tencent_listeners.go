package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"sort"
)

const tencentListenersOperationName = "tencent.clb.describe_listeners"

func tencentListenersOperation() provider.Operation {
	return provider.Operation{Name: tencentListenersOperationName, Provider: model.ProviderTencent, Service: "clb", Description: "List CLB listener metadata; locally paginated by listener ID; excludes rules, certificates and addresses", Parameters: detailParameters("load_balancer_id")}
}

type tencentListenerCursor struct{ Profile, Account, Region, LoadBalancer, LastID string }

func decodeTencentListenerCursor(token string, scope tencentListenerCursor) (string, error) {
	if token == "" {
		return "", nil
	}
	var c tencentListenerCursor
	data, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || json.Unmarshal(data, &c) != nil || c.LastID == "" || c.Profile != scope.Profile || c.Account != scope.Account || c.Region != scope.Region || c.LoadBalancer != scope.LoadBalancer {
		return "", &provider.Error{Code: "invalid_cursor", Operation: tencentListenersOperationName, Message: "listener cursor does not match query scope"}
	}
	return c.LastID, nil
}
func (a *tencentAdapter) readCLBListeners(ctx context.Context, req provider.NativeRequest, s tencentVPCSpec) (provider.Page, error) {
	op := provider.Operation{Name: req.Operation, Parameters: detailParameters(s.param)}
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	id, err := nativeIdentifier(req.Params, s.param)
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if id == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: op.Name, Message: s.param + " is required"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	if _, err := decodeTencentListenerCursor(req.PageToken, tencentListenerCursor{Profile: a.name, Account: account, Region: region, LoadBalancer: id}); err != nil {
		return provider.Page{}, err
	}
	client, err := a.tencentClient("clb.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("clb", "2018-03-17", "DescribeListeners")
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"LoadBalancerId": id}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := client.Send(request, response); err != nil {
		return provider.Page{}, tencentSourceError(op.Name, err)
	}
	var payload struct {
		Response map[string]json.RawMessage `json:"Response"`
	}
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid CLB response"}
	}
	if raw := payload.Response["Error"]; len(raw) > 0 && string(raw) != "null" {
		var e struct{ Code, Message string }
		if json.Unmarshal(raw, &e) != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid CLB error"}
		}
		return provider.Page{}, &provider.Error{Code: e.Code, Operation: op.Name, Message: e.Message}
	}
	var resources []map[string]any
	if err := json.Unmarshal(payload.Response["Listeners"], &resources); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "missing or invalid CLB resource list"}
	}
	return a.tencentListenerPage(resources, req, id, region, account)
}
func (a *tencentAdapter) tencentListenerPage(resources []map[string]any, req provider.NativeRequest, id, region, account string) (provider.Page, error) {
	scope := tencentListenerCursor{Profile: a.name, Account: account, Region: region, LoadBalancer: id}
	last, err := decodeTencentListenerCursor(req.PageToken, scope)
	if err != nil {
		return provider.Page{}, err
	}
	byID := map[string]map[string]any{}
	ids := []string{}
	for _, d := range resources {
		key, ok := d["ListenerId"].(string)
		if !ok || key == "" {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: req.Operation, Message: "listener ID missing"}
		}
		if _, ok := byID[key]; ok {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: req.Operation, Message: "duplicate listener ID"}
		}
		byID[key] = d
		if key > last {
			ids = append(ids, key)
		}
	}
	sort.Strings(ids)
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	page := provider.Page{Rows: []map[string]any{}, Scanned: len(resources), Requests: 1}
	count := len(ids)
	if count > limit {
		count = limit
	}
	for _, key := range ids[:count] {
		d := byID[key]
		name, _ := d["ListenerName"].(string)
		row := newDeepDetailRow(a.Provider(), a.name, key, name, "clb", "QCS::CLB::Listener", "network", "listener", region, account)
		attrs := row["attributes"].(map[string]any)
		attrs["load_balancer_id"] = id
		for from, to := range map[string]string{"Protocol": "protocol", "Port": "port", "EndPort": "end_port", "Scheduler": "scheduler", "SessionExpireTime": "session_expire_seconds", "SniSwitch": "sni_switch", "TargetType": "target_type"} {
			if v, ok := d[from]; ok && v != nil {
				switch v.(type) {
				case string, float64, bool:
					attrs[to] = v
				}
			}
		}
		page.Rows = append(page.Rows, row)
	}
	if len(ids) > count {
		scope.LastID = ids[count-1]
		data, _ := json.Marshal(scope)
		page.NextToken = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}
