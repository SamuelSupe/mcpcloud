package providers

import (
	"context"
	"encoding/json"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const tencentCLBOperationName = "tencent.clb.describe_load_balancer"

var tencentCLBSpec = tencentVPCSpec{param: "load_balancer_id", action: "DescribeLoadBalancers", filter: "LoadBalancerIds", list: "LoadBalancerSet", id: "LoadBalancerId", kind: "load_balancer"}

func tencentCLBOperation() provider.Operation {
	return provider.Operation{Name: tencentCLBOperationName, Provider: model.ProviderTencent, Service: "clb", Description: "Get one CLB instance by ID; excludes addresses, listeners and targets", Parameters: detailParameters("load_balancer_id")}
}
func (a *tencentAdapter) readCLBDetail(ctx context.Context, req provider.NativeRequest, s tencentVPCSpec) (provider.Page, error) {
	op := provider.Operation{Name: req.Operation, Parameters: detailParameters(s.param)}
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	if req.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: op.Name, Message: "single resource detail does not accept a cursor"}
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
	client, err := a.tencentClient("clb.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("clb", "2018-03-17", s.action)
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{s.filter: []string{id}, "Limit": 1}); err != nil {
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
	if err := json.Unmarshal(payload.Response[s.list], &resources); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "missing or invalid CLB resource list"}
	}
	for _, d := range resources {
		if d[s.id] == id {
			return oneDeepDetailPage(a.tencentCLBRow(d, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(op.Name, s.kind)
}
func (a *tencentAdapter) tencentCLBRow(d map[string]any, region, account string) map[string]any {
	text := func(key string) string { v, _ := d[key].(string); return v }
	row := newDeepDetailRow(a.Provider(), a.name, text("LoadBalancerId"), text("LoadBalancerName"), "clb", "QCS::CLB::LoadBalancer", "network", "load_balancer", region, account)
	row["state"] = "unknown"
	if status, ok := d["Status"].(float64); ok {
		row["native"].(map[string]any)["status_code"] = status
		if status == 0 {
			row["state"] = "creating"
		} else if status == 1 {
			row["state"] = "running"
		}
	}
	attrs := row["attributes"].(map[string]any)
	copyFields := func(source map[string]any, fields map[string]string) {
		for from, to := range fields {
			if v, ok := source[from]; ok && v != nil {
				switch v.(type) {
				case string, float64, bool:
					attrs[to] = v
				}
			}
		}
	}
	copyFields(d, map[string]string{"LoadBalancerType": "network_type", "VpcId": "vpc_id", "SubnetId": "subnet_id", "ChargeType": "billing_mode", "ProjectId": "project_id", "AddressIPVersion": "ip_version", "Forward": "forward_type", "SlaType": "sla_type"})
	if network, ok := d["NetworkAttributes"].(map[string]any); ok {
		copyFields(network, map[string]string{"InternetChargeType": "internet_billing_mode", "InternetMaxBandwidthOut": "max_outbound_bandwidth_mbps"})
	}
	tags := map[string]any{}
	if items, ok := d["Tags"].([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				key, _ := m["TagKey"].(string)
				value, valid := m["TagValue"].(string)
				if key != "" && valid {
					tags[key] = value
				}
			}
		}
	}
	row["tags"] = tags
	return row
}
