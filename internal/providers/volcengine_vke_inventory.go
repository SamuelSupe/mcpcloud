package providers

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/vke"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

const (
	volcengineVKEListNodePoolsOperation = "volcengine.vke.list_node_pools"
	volcengineVKEListNodesOperation     = "volcengine.vke.list_nodes"
)

func volcengineVKEInventoryOperations() []provider.Operation {
	parameters := detailParameters("cluster_id")
	return []provider.Operation{
		operation(volcengineVKEListNodePoolsOperation, model.ProviderVolcengine, "vke", "List VKE node pools for one cluster using a fixed cluster ID filter", cloneNativeSchema(parameters)),
		operation(volcengineVKEListNodesOperation, model.ProviderVolcengine, "vke", "List VKE nodes for one cluster using a fixed cluster ID filter", cloneNativeSchema(parameters)),
	}
}

func volcengineVKEInventoryOperationNames() []string {
	return []string{volcengineVKEListNodePoolsOperation, volcengineVKEListNodesOperation}
}

func isVolcengineVKEInventoryOperation(name string) bool {
	return name == volcengineVKEListNodePoolsOperation || name == volcengineVKEListNodesOperation
}

func validateVolcengineVKEInventoryRequest(request provider.NativeRequest) (string, int32, int32, error) {
	var operation provider.Operation
	for _, candidate := range volcengineVKEInventoryOperations() {
		if candidate.Name == request.Operation {
			operation = candidate
			break
		}
	}
	if operation.Name == "" {
		return "", 0, 0, &provider.Error{Code: "operation_not_allowed", Operation: request.Operation, Message: "operation is not registered"}
	}
	if err := operation.ValidateParams(request.Params); err != nil {
		return "", 0, 0, &provider.Error{Code: "invalid_parameter", Operation: request.Operation, Message: err.Error()}
	}
	clusterID, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil {
		return "", 0, 0, deepParameterError(request.Operation, err)
	}
	pageNumber := int32(1)
	if request.PageToken != "" {
		parsed, err := strconv.ParseInt(request.PageToken, 10, 32)
		if err != nil || parsed < 2 || parsed > 1_000_000 {
			return "", 0, 0, &provider.Error{Code: "invalid_cursor", Operation: request.Operation, Message: "VKE page cursor is invalid"}
		}
		pageNumber = int32(parsed)
	}
	limit := request.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return clusterID, pageNumber, int32(limit), nil
}

func (a *volcengineAdapter) readVKEInventory(ctx context.Context, request provider.NativeRequest) (provider.Page, error) {
	clusterID, pageNumber, pageSize, err := validateVolcengineVKEInventoryRequest(request)
	if err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, request.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(request.Region, a.profile.Regions, request.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	sess, err := a.session(region, request.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	client := newVolcengineVKEDetailClient(sess)
	if request.Operation == volcengineVKEListNodePoolsOperation {
		output, err := client.ListNodePoolsWithContext(ctx, &vke.ListNodePoolsInput{
			Filter: &vke.FilterForListNodePoolsInput{ClusterIds: []*string{volc.String(clusterID)}}, PageNumber: &pageNumber, PageSize: &pageSize,
		})
		if err != nil {
			return provider.Page{}, volcengineDeepError(request.Operation, err)
		}
		if output == nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: request.Operation, Message: "VKE ListNodePools returned no response"}
		}
		rows := make([]map[string]any, 0, len(output.Items))
		for _, item := range output.Items {
			if item != nil && volc.StringValue(item.ClusterId) == clusterID {
				rows = append(rows, a.volcengineVKENodePoolRow(item, region, account))
			}
		}
		return volcengineVKEListPage(rows, pageNumber, pageSize, volc.Int32Value(output.TotalCount)), nil
	}
	output, err := client.ListNodesWithContext(ctx, &vke.ListNodesInput{
		Filter: &vke.FilterForListNodesInput{ClusterIds: []*string{volc.String(clusterID)}}, PageNumber: &pageNumber, PageSize: &pageSize,
	})
	if err != nil {
		return provider.Page{}, volcengineDeepError(request.Operation, err)
	}
	if output == nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: request.Operation, Message: "VKE ListNodes returned no response"}
	}
	rows := make([]map[string]any, 0, len(output.Items))
	for _, item := range output.Items {
		if item != nil && volc.StringValue(item.ClusterId) == clusterID {
			rows = append(rows, a.volcengineVKENodeRow(item, region, account))
		}
	}
	return volcengineVKEListPage(rows, pageNumber, pageSize, volc.Int32Value(output.TotalCount)), nil
}

func volcengineVKEListPage(rows []map[string]any, pageNumber, pageSize, total int32) provider.Page {
	next := ""
	if pageNumber*pageSize < total {
		next = strconv.FormatInt(int64(pageNumber+1), 10)
	}
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}
}

func (a *volcengineAdapter) volcengineVKENodePoolRow(detail *vke.ItemForListNodePoolsOutput, region, account string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(detail.Id), volc.StringValue(detail.Name), "vke", "VKE::NodePool", "kubernetes", "node_pool", region, account)
	if detail.Status != nil {
		row["state"] = strings.ToLower(volc.StringValue(detail.Status.Phase))
	}
	setDetailTime(row, "created_at", volc.StringValue(detail.CreateTime), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", volc.StringValue(detail.UpdateTime), time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["cluster_id"] = volc.StringValue(detail.ClusterId)
	if detail.NodeStatistics != nil {
		attributes["node_count"] = int(volc.Int32Value(detail.NodeStatistics.TotalCount))
		attributes["running_node_count"] = int(volc.Int32Value(detail.NodeStatistics.RunningCount))
		attributes["creating_node_count"] = int(volc.Int32Value(detail.NodeStatistics.CreatingCount))
		attributes["updating_node_count"] = int(volc.Int32Value(detail.NodeStatistics.UpdatingCount))
		attributes["deleting_node_count"] = int(volc.Int32Value(detail.NodeStatistics.DeletingCount))
		attributes["failed_node_count"] = int(volc.Int32Value(detail.NodeStatistics.FailedCount))
	}
	if detail.AutoScaling != nil {
		attributes["desired_nodes"] = int(volc.Int32Value(detail.AutoScaling.DesiredReplicas))
		attributes["minimum_nodes"] = int(volc.Int32Value(detail.AutoScaling.MinReplicas))
		attributes["maximum_nodes"] = int(volc.Int32Value(detail.AutoScaling.MaxReplicas))
		setPosture(row, "auto_scaling_enabled", volc.BoolValue(detail.AutoScaling.Enabled))
	}
	if detail.NodeConfig != nil {
		attributes["billing_mode"] = volc.StringValue(detail.NodeConfig.InstanceChargeType)
		attributes["image_id"] = volc.StringValue(detail.NodeConfig.ImageId)
		setRelated(row, "instance_type_ids", volcStringValues(detail.NodeConfig.InstanceTypeIds))
		setRelated(row, "subnet_ids", volcStringValues(detail.NodeConfig.SubnetIds))
		setPosture(row, "public_access_enabled", volc.BoolValue(detail.NodeConfig.PublicAccessEnabled))
	}
	return row
}

func (a *volcengineAdapter) volcengineVKENodeRow(detail *vke.ItemForListNodesOutput, region, account string) map[string]any {
	name := volc.StringValue(detail.MetadataName)
	if name == "" {
		name = volc.StringValue(detail.Name)
	}
	row := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(detail.Id), name, "vke", "VKE::Node", "kubernetes", "node", region, account)
	if detail.Status != nil {
		row["state"] = strings.ToLower(volc.StringValue(detail.Status.Phase))
	}
	row["zone"] = volc.StringValue(detail.ZoneId)
	setDetailTime(row, "created_at", volc.StringValue(detail.CreateTime), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", volc.StringValue(detail.UpdateTime), time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["cluster_id"] = volc.StringValue(detail.ClusterId)
	attributes["node_pool_id"] = volc.StringValue(detail.NodePoolId)
	attributes["instance_id"] = volc.StringValue(detail.InstanceId)
	attributes["image_id"] = volc.StringValue(detail.ImageId)
	attributes["virtual_node"] = volc.BoolValue(detail.IsVirtual)
	setRelated(row, "roles", volcStringValues(detail.Roles))
	setPosture(row, "additional_container_storage_enabled", volc.BoolValue(detail.AdditionalContainerStorageEnabled))
	if detail.KubernetesConfig != nil {
		setPosture(row, "cordoned", volc.BoolValue(detail.KubernetesConfig.Cordon))
	}
	return row
}
