package providers

import (
	"context"
	"encoding/json"
	"strings"

	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type tencentVPCSpec struct {
	param, action, filter, list, id, name, state, kind string
	fields                                             map[string]string
}

var tencentVPCSpecs = map[string]tencentVPCSpec{
	"tencent.vpc.describe_address":     {param: "address_id", action: "DescribeAddresses", filter: "AddressIds", list: "AddressSet", id: "AddressId", name: "AddressName", state: "AddressStatus", kind: "public_ip", fields: map[string]string{"InstanceId": "bound_resource_id", "InstanceType": "bound_resource_type", "Bandwidth": "bandwidth_limit", "InternetChargeType": "billing_mode", "BandwidthPackageId": "bandwidth_package_id"}},
	"tencent.vpc.describe_nat_gateway": {param: "nat_gateway_id", action: "DescribeNatGateways", filter: "NatGatewayIds", list: "NatGatewaySet", id: "NatGatewayId", name: "NatGatewayName", state: "State", kind: "nat_gateway", fields: map[string]string{"VpcId": "vpc_id", "SubnetId": "subnet_id", "NetworkState": "network_state", "NatType": "nat_type", "InternetMaxBandwidthOut": "max_outbound_bandwidth_mbps", "MaxConcurrentConnection": "max_concurrent_connections", "DeletionProtectionEnabled": "deletion_protection"}},
}

func tencentVPCOperations() []provider.Operation {
	ops := []provider.Operation{}
	for _, name := range []string{"tencent.vpc.describe_address", "tencent.vpc.describe_nat_gateway"} {
		s := tencentVPCSpecs[name]
		ops = append(ops, provider.Operation{Name: name, Provider: model.ProviderTencent, Service: "vpc", Description: "Get one " + s.kind + " by ID; excludes IP addresses and embedded NAT rules", Parameters: detailParameters(s.param)})
	}
	return ops
}
func (a *tencentAdapter) readVPCDetail(ctx context.Context, req provider.NativeRequest, s tencentVPCSpec) (provider.Page, error) {
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
	client, err := a.tencentClient("vpc.tencentcloudapi.com", region, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	request := tchttp.NewCommonRequest("vpc", "2017-03-12", s.action)
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
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid VPC response"}
	}
	if raw := payload.Response["Error"]; len(raw) > 0 && string(raw) != "null" {
		var e struct{ Code, Message string }
		if json.Unmarshal(raw, &e) != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid VPC error"}
		}
		return provider.Page{}, &provider.Error{Code: e.Code, Operation: op.Name, Message: e.Message}
	}
	var resources []map[string]any
	if err := json.Unmarshal(payload.Response[s.list], &resources); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "missing or invalid VPC resource list"}
	}
	for _, d := range resources {
		if d[s.id] == id {
			return oneDeepDetailPage(a.tencentVPCRow(d, s, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(op.Name, s.kind)
}
func (a *tencentAdapter) tencentVPCRow(d map[string]any, s tencentVPCSpec, region, account string) map[string]any {
	text := func(key string) string { v, _ := d[key].(string); return v }
	nativeType := "QCS::VPC::Address"
	if s.kind == "nat_gateway" {
		nativeType = "QCS::VPC::NatGateway"
	}
	row := newDeepDetailRow(a.Provider(), a.name, text(s.id), text(s.name), "vpc", nativeType, "network", s.kind, region, account)
	row["state"] = strings.ToLower(text(s.state))
	attrs := row["attributes"].(map[string]any)
	for source, target := range s.fields {
		if value, ok := d[source]; ok && value != nil {
			switch value.(type) {
			case string, float64, bool:
				attrs[target] = value
			}
		}
	}
	if s.kind == "nat_gateway" {
		ids := []string{}
		if items, ok := d["PublicIpAddressSet"].([]any); ok {
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					if id, ok := m["AddressId"].(string); ok {
						ids = append(ids, id)
					}
				}
			}
		}
		setRelated(row, "eip_ids", ids)
	}
	tags := map[string]any{}
	if items, ok := d["TagSet"].([]any); ok {
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				key, _ := m["Key"].(string)
				value, valid := m["Value"].(string)
				if key != "" && valid {
					tags[key] = value
				}
			}
		}
	}
	row["tags"] = tags
	return row
}
