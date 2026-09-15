package providers

import (
	"context"
	"strings"

	ces "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1"
	cesmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1/model"
	cesregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ces/v1/region"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func huaweiCESOperations() []provider.Operation {
	return []provider.Operation{operation(huaweiCESListMetricsOperation, model.ProviderHuawei, "monitoring", "Discover monitored CES metric definitions and exact dimensions", map[string]any{
		"namespace":   map[string]any{"type": "string", "required": true},
		"metric_name": map[string]any{"type": "string"},
	})}
}

func (a *huaweiAdapter) listHuaweiCESMetrics(ctx context.Context, r provider.NativeRequest) (provider.Page, error) {
	if r.PageToken != "" {
		return provider.Page{}, &provider.Error{Code: "cursor_not_supported", Operation: r.Operation, Message: "CES metric discovery does not expose provider cursors"}
	}
	namespace, err := nativeString(r.Params, "namespace")
	if err != nil || namespace == "" {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: r.Operation, Message: "CES metric discovery requires namespace"}
	}
	metricName, err := nativeString(r.Params, "metric_name")
	if err != nil || len(r.Params) > 2 {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: r.Operation, Message: "CES metric discovery accepts only namespace and optional metric_name"}
	}
	if len(a.profile.Scopes.Projects) != 1 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: r.Operation, Message: "CES metric discovery requires exactly one configured project"}
	}
	regionID, err := exactDetailRegion(r.Region, a.profile.Regions, r.Operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := cesregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: r.Operation, Message: "CES region is invalid"}
	}
	project := scopeTail(a.profile.Scopes.Projects[0])
	credential, err := a.huaweiBasicCredential(r.Operation, project)
	if err != nil {
		return provider.Page{}, err
	}
	hc, err := ces.CesClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: r.Operation, Message: "CES client could not be built"}
	}
	limit := int32(1000)
	request := &cesmodel.ListMetricsRequest{Namespace: &namespace, MetricName: optionalString(metricName), Limit: &limit}
	response, err := ces.NewCesClient(hc).ListMetrics(request)
	if err != nil {
		return provider.Page{}, huaweiSourceError(r.Operation, err)
	}
	rows := []map[string]any{}
	if response.Metrics != nil {
		for _, metric := range *response.Metrics {
			dimensions := map[string]string{}
			for _, dimension := range metric.Dimensions {
				dimensions[deref(dimension.Name)] = deref(dimension.Value)
			}
			id := metric.MetricName + ":" + firstDimension(dimensions)
			row := newDeepDetailRow(a.Provider(), a.name, id, metric.MetricName, "monitoring", "CES::Metric", "monitoring", "metric_definition", regionID, project)
			attrs := row["attributes"].(map[string]any)
			attrs["namespace"] = metric.Namespace
			attrs["metric_name"] = metric.MetricName
			attrs["unit"] = metric.Unit
			attrs["dimensions"] = dimensions
			rows = append(rows, row)
		}
	}
	return provider.Page{Rows: rows, Scanned: len(rows), Requests: 1}, nil
}

func optionalString(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}
