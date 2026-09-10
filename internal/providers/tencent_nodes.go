package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"

	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const tencentNodesOperationName = "tencent.tke.describe_cluster_instances"

type tencentNodeCursor struct {
	Offset  int
	Cluster string
	Region  string
	Account string
}

func decodeTencentNodeCursor(token, cluster, region, account string) (int, error) {
	if token == "" {
		return 0, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(token)
	var c tencentNodeCursor
	if err != nil || json.Unmarshal(data, &c) != nil || c.Offset <= 0 || c.Offset > 10000000 || c.Cluster != cluster || c.Region != region || c.Account != account {
		return 0, &provider.Error{Code: "invalid_cursor", Operation: tencentNodesOperationName, Message: "node cursor does not match query scope"}
	}
	return c.Offset, nil
}

func tencentNodesOperation() provider.Operation {
	return provider.Operation{Name: tencentNodesOperationName, Provider: model.ProviderTencent, Service: "tke", Description: "List cluster nodes through the TKE 2022-05-01 API; returns only allow-listed metadata, with pagination", Parameters: detailParameters("cluster_id")}
}

type tencentNodeResponse struct {
	Response struct {
		TotalCount  int `json:"TotalCount"`
		InstanceSet []struct {
			ID    string `json:"InstanceId"`
			State string `json:"InstanceState"`
			Role  string `json:"InstanceRole"`
			Type  string `json:"NodeType"`
			Pool  string `json:"NodePoolId"`
		} `json:"InstanceSet"`
		Errors []string `json:"Errors"`
		Error  *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

func (a *tencentAdapter) readClusterNodes(ctx context.Context, req provider.NativeRequest) (provider.Page, error) {
	op := tencentNodesOperation()
	if err := op.ValidateParams(req.Params); err != nil {
		return provider.Page{}, err
	}
	id, err := nativeIdentifier(req.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(op.Name, err)
	}
	if id == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: op.Name, Message: "cluster_id is required"}
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	offset, err := decodeTencentNodeCursor(req.PageToken, id, region, account)
	if err != nil {
		return provider.Page{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, op.Name)
	}
	request := tchttp.NewCommonRequest("tke", "2022-05-01", "DescribeClusterInstances")
	request.SetContext(ctx)
	if err := request.SetActionParameters(map[string]any{"ClusterId": id, "Offset": offset, "Limit": limit}); err != nil {
		return provider.Page{}, err
	}
	response := tchttp.NewCommonResponse()
	if err := newTencentTKEDetailClient(credential, region).Send(request, response); err != nil {
		return provider.Page{}, tencentDeepError(op.Name, err)
	}
	var payload tencentNodeResponse
	if err := json.Unmarshal(response.GetBody(), &payload); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: op.Name, Message: "invalid TKE node response"}
	}
	return a.tencentNodePage(payload, id, region, account, offset)
}

func (a *tencentAdapter) tencentNodePage(payload tencentNodeResponse, cluster, region, account string, offset int) (provider.Page, error) {
	if err := tencentEnvelopeError(tencentNodesOperationName, payload.Response.Error); err != nil {
		return provider.Page{}, err
	}
	// Errors may contain internal addresses or upstream request details. Do not copy them into output.
	if len(payload.Response.Errors) > 0 {
		return provider.Page{}, &provider.Error{Code: "incomplete_provider_response", Operation: tencentNodesOperationName, Message: "TKE reported incomplete node information"}
	}
	rows := make([]map[string]any, 0, len(payload.Response.InstanceSet))
	for _, node := range payload.Response.InstanceSet {
		if node.ID == "" {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: tencentNodesOperationName, Message: "TKE returned a node without an ID"}
		}
		row := newDeepDetailRow(a.Provider(), a.name, node.ID, "", "tke", "QCS::TKE::Node", "kubernetes", "node", region, account)
		row["state"] = node.State
		attrs := row["attributes"].(map[string]any)
		attrs["cluster_id"], attrs["node_pool_id"] = cluster, node.Pool
		attrs["node_type"], attrs["role"] = node.Type, node.Role
		rows = append(rows, row)
	}
	next := ""
	if offset+len(rows) < payload.Response.TotalCount {
		if len(rows) == 0 {
			return provider.Page{}, &provider.Error{Code: "incomplete_provider_response", Operation: tencentNodesOperationName, Message: "TKE returned an empty page before the reported total"}
		}
		data, _ := json.Marshal(tencentNodeCursor{Offset: offset + len(rows), Cluster: cluster, Region: region, Account: account})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}
