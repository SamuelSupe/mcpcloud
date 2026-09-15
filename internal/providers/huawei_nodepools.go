package providers

import (
	"context"
	"strings"

	cce "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3"
	ccemodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	cceregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/region"

	"mcpcloud/internal/provider"
)

// listCCENodePools deliberately exposes only pool identity, state and capacity
// counters. It never returns bootstrap scripts, labels, annotations or job IDs.
func (a *huaweiAdapter) listCCENodePools(ctx context.Context, request provider.NativeRequest) (provider.Page, error) {
	spec := provider.Operation{Name: huaweiCCENodePoolsOperation, Parameters: detailParameters("project_id", "cluster_id")}
	if err := spec.ValidateParams(request.Params); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(huaweiCCENodePoolsOperation, err)
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, projectRaw, huaweiCCENodePoolsOperation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	clusterID, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil || clusterID == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: huaweiCCENodePoolsOperation, Message: "parameter cluster_id is required"}
	}
	if request.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: huaweiCCENodePoolsOperation, Message: "CCE node pool API does not provide a page cursor"}
	}
	regionID, err := exactDetailRegion(request.Region, a.profile.Regions, huaweiCCENodePoolsOperation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := cceregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: huaweiCCENodePoolsOperation, Message: err.Error()}
	}
	credential, err := a.huaweiBasicCredential(huaweiCCENodePoolsOperation, project)
	if err != nil {
		return provider.Page{}, err
	}
	hc, err := cce.CceClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiCCENodePoolsOperation, Message: err.Error()}
	}
	response, err := cce.NewCceClient(hc).ListNodePools(&ccemodel.ListNodePoolsRequest{ClusterId: clusterID})
	if err != nil {
		return provider.Page{}, huaweiDeepError(huaweiCCENodePoolsOperation, err)
	}
	rows := []map[string]any{}
	if response != nil && response.Items != nil {
		for _, pool := range *response.Items {
			if pool.Metadata == nil || pool.Metadata.Uid == nil || *pool.Metadata.Uid == "" {
				continue
			}
			row := newDeepDetailRow(a.Provider(), a.name, *pool.Metadata.Uid, pool.Metadata.Name, "cce", "CCE::NodePool", "kubernetes", "node_pool", regionID, project)
			attrs := row["attributes"].(map[string]any)
			attrs["cluster_id"] = clusterID
			if pool.Status != nil {
				if pool.Status.Phase != nil {
					row["state"] = strings.ToLower(pool.Status.Phase.Value())
				}
				if pool.Status.CurrentNode != nil {
					attrs["node_count"] = *pool.Status.CurrentNode
				}
				if pool.Status.ActiveNode != nil {
					attrs["active_node_count"] = *pool.Status.ActiveNode
				}
				if pool.Status.CreatingNode != nil {
					attrs["creating_node_count"] = *pool.Status.CreatingNode
				}
				if pool.Status.DeletingNode != nil {
					attrs["deleting_node_count"] = *pool.Status.DeletingNode
				}
			}
			rows = append(rows, row)
		}
	}
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}
