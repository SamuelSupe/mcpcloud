package providers

import (
	"context"
	"strconv"
	"strings"

	dcs "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dcs/v2"
	dcsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dcs/v2/model"
	dcsregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/dcs/v2/region"
	vpc "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/vpc/v2"
	vpcmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/vpc/v2/model"
	vpcregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/vpc/v2/region"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func huaweiNetworkAndDCSOperations() []provider.Operation {
	return []provider.Operation{
		operation(huaweiVPCListOperation, model.ProviderHuawei, "vpc", "List Huawei VPCs", detailParameters("project_id")),
		operation(huaweiSubnetListOperation, model.ProviderHuawei, "vpc", "List Huawei subnets", detailParameters("project_id")),
		operation(huaweiSecurityGroupListOperation, model.ProviderHuawei, "vpc", "List Huawei security groups", detailParameters("project_id")),
		operation(huaweiDCSListOperation, model.ProviderHuawei, "dcs", "List Huawei DCS instances without addresses or users", detailParameters("project_id")),
	}
}
func isHuaweiNetworkOrDCSOperation(op string) bool {
	return op == huaweiVPCListOperation || op == huaweiSubnetListOperation || op == huaweiSecurityGroupListOperation || op == huaweiDCSListOperation
}
func (a *huaweiAdapter) listHuaweiNetworkOrDCS(ctx context.Context, r provider.NativeRequest) (provider.Page, error) {
	spec := provider.Operation{Name: r.Operation, Parameters: detailParameters("project_id")}
	if err := spec.ValidateParams(r.Params); err != nil {
		return provider.Page{}, err
	}
	raw, err := nativeIdentifier(r.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(r.Operation, err)
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, raw, r.Operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	regionID, err := exactDetailRegion(r.Region, a.profile.Regions, r.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	cred, err := a.huaweiBasicCredential(r.Operation, project)
	if err != nil {
		return provider.Page{}, err
	}
	if r.Operation == huaweiDCSListOperation {
		region, e := dcsregion.SafeValueOf(regionID)
		if e != nil {
			return provider.Page{}, e
		}
		hc, e := dcs.DcsClientBuilder().WithRegion(region).WithCredential(cred).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
		if e != nil {
			return provider.Page{}, e
		}
		limit := nativeHuaweiListLimit(r.Limit, 1000)
		offset, err := huaweiDCSOffset(r.Operation, r.PageToken)
		if err != nil {
			return provider.Page{}, err
		}
		out, e := dcs.NewDcsClient(hc).ListInstances(&dcsmodel.ListInstancesRequest{Limit: &limit, Offset: &offset})
		if e != nil {
			return provider.Page{}, huaweiDeepError(r.Operation, e)
		}
		rows := []map[string]any{}
		if out.Instances != nil {
			for _, x := range *out.Instances {
				if x.InstanceId == nil {
					continue
				}
				row := newDeepDetailRow(a.Provider(), a.name, *x.InstanceId, deref(x.Name), "dcs", "DCS::Instance", "database", "cache", regionID, project)
				row["state"] = strings.ToLower(deref(x.Status))
				at := row["attributes"].(map[string]any)
				at["engine"] = strings.ToLower(deref(x.Engine))
				at["engine_version"] = deref(x.EngineVersion)
				if x.Capacity != nil {
					at["capacity_gb"] = *x.Capacity
				}
				if x.MaxMemory != nil {
					at["memory_mb"] = *x.MaxMemory
				}
				if x.UsedMemory != nil {
					at["memory_used_mb"] = *x.UsedMemory
				}
				at["vpc_id"] = deref(x.VpcId)
				at["subnet_id"] = deref(x.SubnetId)
				setRelated(row, "security_group_ids", []string{deref(x.SecurityGroupId)})
				rows = append(rows, row)
			}
		}
		next := ""
		if out.InstanceNum != nil && int64(offset)+int64(len(rows)) < int64(*out.InstanceNum) {
			next = strconv.FormatInt(int64(offset)+int64(len(rows)), 10)
		}
		return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
	}
	region, e := vpcregion.SafeValueOf(regionID)
	if e != nil {
		return provider.Page{}, e
	}
	hc, e := vpc.VpcClientBuilder().WithRegion(region).WithCredential(cred).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if e != nil {
		return provider.Page{}, e
	}
	client := vpc.NewVpcClient(hc)
	limit := nativeHuaweiListLimit(r.Limit, 200)
	rows := []map[string]any{}
	next := ""
	switch r.Operation {
	case huaweiVPCListOperation:
		request := &vpcmodel.ListVpcsRequest{Limit: &limit}
		if r.PageToken != "" {
			request.Marker = &r.PageToken
		}
		out, e := client.ListVpcs(request)
		if e != nil {
			return provider.Page{}, huaweiDeepError(r.Operation, e)
		}
		if out.Vpcs != nil {
			for _, x := range *out.Vpcs {
				row := newDeepDetailRow(a.Provider(), a.name, x.Id, x.Name, "vpc", "VPC::Vpc", "network", "network", regionID, project)
				row["state"] = strings.ToLower(x.Status.Value())
				at := row["attributes"].(map[string]any)
				at["cidr"] = x.Cidr
				at["enterprise_project_id"] = x.EnterpriseProjectId
				rows = append(rows, row)
			}
		}
		next = huaweiMarkerNext(rows, int(limit))
	case huaweiSubnetListOperation:
		request := &vpcmodel.ListSubnetsRequest{Limit: &limit}
		if r.PageToken != "" {
			request.Marker = &r.PageToken
		}
		out, e := client.ListSubnets(request)
		if e != nil {
			return provider.Page{}, huaweiDeepError(r.Operation, e)
		}
		if out.Subnets != nil {
			for _, x := range *out.Subnets {
				row := newDeepDetailRow(a.Provider(), a.name, x.Id, x.Name, "vpc", "VPC::Subnet", "network", "subnet", regionID, project)
				row["state"] = strings.ToLower(x.Status.Value())
				at := row["attributes"].(map[string]any)
				at["cidr"] = x.Cidr
				at["vpc_id"] = x.VpcId
				rows = append(rows, row)
			}
		}
		next = huaweiMarkerNext(rows, int(limit))
	case huaweiSecurityGroupListOperation:
		request := &vpcmodel.ListSecurityGroupsRequest{Limit: &limit}
		if r.PageToken != "" {
			request.Marker = &r.PageToken
		}
		out, e := client.ListSecurityGroups(request)
		if e != nil {
			return provider.Page{}, huaweiDeepError(r.Operation, e)
		}
		if out.SecurityGroups != nil {
			for _, x := range *out.SecurityGroups {
				row := newDeepDetailRow(a.Provider(), a.name, x.Id, x.Name, "vpc", "VPC::SecurityGroup", "network", "security_group", regionID, project)
				row["state"] = "active"
				at := row["attributes"].(map[string]any)
				at["vpc_id"] = x.VpcId
				at["enterprise_project_id"] = x.EnterpriseProjectId
				rows = append(rows, row)
			}
		}
		next = huaweiMarkerNext(rows, int(limit))
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func nativeHuaweiListLimit(requested, maximum int) int32 {
	if requested <= 0 || requested > maximum {
		return int32(maximum)
	}
	return int32(requested)
}

func huaweiDCSOffset(operation, token string) (int32, error) {
	if token == "" {
		return 0, nil
	}
	offset, err := strconv.ParseInt(token, 10, 32)
	if err != nil || offset < 0 {
		return 0, &provider.Error{Code: "invalid_page_token", Operation: operation, Message: "DCS page token must be a non-negative decimal offset"}
	}
	return int32(offset), nil
}

func huaweiMarkerNext(rows []map[string]any, limit int) string {
	if len(rows) != limit || len(rows) == 0 {
		return ""
	}
	id := stringValue(rows[len(rows)-1]["id"])
	if id == "" {
		return ""
	}
	return id
}
