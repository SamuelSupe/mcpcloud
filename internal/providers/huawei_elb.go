package providers

import (
	"context"
	"encoding/json"
	"strings"

	elb "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/elb/v3"
	elbmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/elb/v3/model"
	elbregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/elb/v3/region"

	"mcpcloud/internal/provider"
)

func (a *huaweiAdapter) listELBResources(ctx context.Context, request provider.NativeRequest) (provider.Page, error) {
	parameterNames := []string{"project_id"}
	if request.Operation == huaweiELBMembersOperation {
		parameterNames = append(parameterNames, "pool_id")
	}
	spec := provider.Operation{Name: request.Operation, Parameters: detailParameters(parameterNames...)}
	if err := spec.ValidateParams(request.Params); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(request.Operation, err)
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, projectRaw, request.Operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	regionID, err := exactDetailRegion(request.Region, a.profile.Regions, request.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	credential, err := a.huaweiBasicCredential(request.Operation, project)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := elbregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: request.Operation, Message: err.Error()}
	}
	hc, err := elb.ElbClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: request.Operation, Message: err.Error()}
	}
	client := elb.NewElbClient(hc)
	limit := request.Limit
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	limit32 := int32(limit)
	var payload any
	var next string
	switch request.Operation {
	case huaweiELBListOperation:
		r := &elbmodel.ListLoadBalancersRequest{Limit: &limit32}
		if request.PageToken != "" {
			r.Marker = &request.PageToken
		}
		response, callErr := client.ListLoadBalancers(r)
		if callErr != nil {
			return provider.Page{}, huaweiDeepError(request.Operation, callErr)
		}
		payload = response.Loadbalancers
		if response.PageInfo != nil && response.PageInfo.NextMarker != nil {
			next = *response.PageInfo.NextMarker
		}
	case huaweiELBListenersOperation:
		r := &elbmodel.ListListenersRequest{Limit: &limit32}
		if request.PageToken != "" {
			r.Marker = &request.PageToken
		}
		response, callErr := client.ListListeners(r)
		if callErr != nil {
			return provider.Page{}, huaweiDeepError(request.Operation, callErr)
		}
		payload = response.Listeners
		if response.PageInfo != nil && response.PageInfo.NextMarker != nil {
			next = *response.PageInfo.NextMarker
		}
	case huaweiELBPoolsOperation:
		r := &elbmodel.ListPoolsRequest{Limit: &limit32}
		if request.PageToken != "" {
			r.Marker = &request.PageToken
		}
		response, callErr := client.ListPools(r)
		if callErr != nil {
			return provider.Page{}, huaweiDeepError(request.Operation, callErr)
		}
		payload = response.Pools
		if response.PageInfo != nil && response.PageInfo.NextMarker != nil {
			next = *response.PageInfo.NextMarker
		}
	case huaweiELBMembersOperation:
		poolID, paramErr := nativeIdentifier(request.Params, "pool_id")
		if paramErr != nil {
			return provider.Page{}, deepParameterError(request.Operation, paramErr)
		}
		r := &elbmodel.ListMembersRequest{PoolId: poolID, Limit: &limit32}
		if request.PageToken != "" {
			r.Marker = &request.PageToken
		}
		response, callErr := client.ListMembers(r)
		if callErr != nil {
			return provider.Page{}, huaweiDeepError(request.Operation, callErr)
		}
		payload = response.Members
		if response.PageInfo != nil && response.PageInfo.NextMarker != nil {
			next = *response.PageInfo.NextMarker
		}
	}
	data, _ := json.Marshal(payload)
	var items []map[string]any
	_ = json.Unmarshal(data, &items)
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		id, name := stringValue(item["id"]), stringValue(item["name"])
		kind := "load_balancer"
		if request.Operation == huaweiELBListenersOperation {
			kind = "listener"
		}
		if request.Operation == huaweiELBPoolsOperation {
			kind = "backend_pool"
		}
		if request.Operation == huaweiELBMembersOperation {
			kind = "backend_member"
		}
		row := newDeepDetailRow(a.Provider(), a.name, id, name, "elb", "ELB::"+strings.ToUpper(kind), "network", kind, regionID, project)
		state := stringValue(item["operating_status"])
		if state == "" {
			state = stringValue(item["provisioning_status"])
		}
		row["state"] = strings.ToLower(state)
		attrs := row["attributes"].(map[string]any)
		for _, key := range []string{"protocol", "protocol_port", "lb_algorithm", "provider", "default_pool_id", "healthmonitor_id", "guaranteed"} {
			if v, ok := item[key]; ok && v != nil && stringValue(v) != "" {
				attrs[key] = v
			}
		}
		if request.Operation == huaweiELBMembersOperation {
			for _, key := range []string{"admin_state_up", "weight", "member_type", "instance_id", "availability_zone"} {
				if v, ok := item[key]; ok && v != nil && stringValue(v) != "" {
					attrs[key] = v
				}
			}
			if poolID, paramErr := nativeIdentifier(request.Params, "pool_id"); paramErr == nil {
				setRelated(row, "pool_ids", []string{poolID})
			}
			if statuses, ok := item["status"].([]any); ok {
				health := make([]map[string]any, 0, len(statuses))
				for _, raw := range statuses {
					if status, ok := raw.(map[string]any); ok {
						health = append(health, map[string]any{"listener_id": stringValue(status["listener_id"]), "operating_status": stringValue(status["operating_status"])})
					}
				}
				if len(health) > 0 {
					attrs["health"] = health
				}
			}
		}
		for _, key := range []string{"loadbalancers", "listeners", "pools", "members"} {
			if ids := huaweiELBIDs(item[key]); len(ids) > 0 {
				setRelated(row, key, ids)
			}
		}
		rows = append(rows, row)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(items), Requests: 1}, nil
}

func huaweiELBIDs(value any) []string {
	items, _ := value.([]any)
	result := []string{}
	for _, raw := range items {
		if item, ok := raw.(map[string]any); ok {
			if id := stringValue(item["id"]); id != "" {
				result = append(result, id)
			}
		}
	}
	return uniqueText(result, 2000)
}
