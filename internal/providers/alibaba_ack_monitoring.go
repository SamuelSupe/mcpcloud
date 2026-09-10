package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/requests"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/responses"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/cs"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

const alibabaACKMonitoringOperation = "alibaba.cs.get_metrics_server_config"

type alibabaACKMonitoringAPI interface {
	DescribeClusterDetail(*cs.DescribeClusterDetailRequest) (*cs.DescribeClusterDetailResponse, error)
	ProcessCommonRequest(*requests.CommonRequest) (*responses.CommonResponse, error)
}

var newAlibabaACKMonitoringClient = func(p config.Profile, region string) (alibabaACKMonitoringAPI, error) {
	c, err := newAlibabaCSDetailClient(p, region)
	if err != nil {
		return nil, err
	}
	client, ok := c.(alibabaACKMonitoringAPI)
	if !ok {
		return nil, fmt.Errorf("ACK client does not support component reads")
	}
	return client, nil
}

func (a *alibabaAdapter) alibabaACKMonitoring(ctx context.Context, spec deepDetailSpec, req provider.NativeRequest, account, region string) (provider.Page, error) {
	id, err := nativeIdentifier(req.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	c, err := newAlibabaACKMonitoringClient(a.profile, region)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	// The addon response has no cluster/region identity. Verify it before reading configuration.
	r := cs.CreateDescribeClusterDetailRequest()
	r.ClusterId = id
	setAlibabaROATimeout(ctx, r.SetReadTimeout, r.SetConnectTimeout)
	p, err := c.DescribeClusterDetail(r)
	if err != nil {
		return provider.Page{}, alibabaDeepError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{Requests: 1}, err
	}
	var cluster alibabaACKDetailResponse
	if p == nil || p.BaseResponse == nil || json.Unmarshal(p.GetHttpContentBytes(), &cluster) != nil || cluster.ClusterID != id || cluster.RegionID != region {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, "cluster")
	}
	request := requests.NewCommonRequest()
	request.Method, request.Scheme = "GET", "https"
	request.Product, request.Version, request.ApiName = "CS", "2015-12-15", "GetClusterAddonInstance"
	request.ServiceCode, request.EndpointType = "cs", "regional"
	request.PathPattern = "/clusters/" + url.PathEscape(id) + "/addon_instances/metrics-server"
	setAlibabaROATimeout(ctx, request.SetReadTimeout, request.SetConnectTimeout)
	response, err := c.ProcessCommonRequest(request)
	if err != nil {
		return provider.Page{Requests: 2}, alibabaDeepError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{Requests: 2}, err
	}
	var addon struct {
		Name    string          `json:"name"`
		State   string          `json:"state"`
		Version string          `json:"version"`
		Config  json.RawMessage `json:"config"`
	}
	invalid := func() (provider.Page, error) {
		return provider.Page{Requests: 2}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: "ACK metrics-server response is invalid"}
	}
	if response == nil || response.BaseResponse == nil || json.Unmarshal(response.GetHttpContentBytes(), &addon) != nil || addon.Name != "metrics-server" {
		return invalid()
	}
	// ACK returns config as either JSON text or an object. Never return raw configuration.
	data := addon.Config
	var text string
	if len(data) > 0 && data[0] == '"' {
		if json.Unmarshal(data, &text) != nil {
			return invalid()
		}
		data = []byte(text)
	}
	var cfg map[string]json.RawMessage
	if len(data) > 0 && json.Unmarshal(data, &cfg) != nil {
		return invalid()
	}
	cms := "unknown"
	if value, exists := cfg["CmsEnabled"]; exists {
		switch string(value) {
		case "true", `"true"`:
			cms = "enabled"
		case "false", `"false"`:
			cms = "disabled"
		}
	}
	row := a.alibabaACKDetailRow(cluster, account, region)
	row["attributes"].(map[string]any)["metrics_server"] = map[string]any{
		"name": addon.Name, "state": addon.State, "version": addon.Version,
		"cloudmonitor_collection": cms, "source": "GetClusterAddonInstance",
	}
	return provider.Page{Rows: []map[string]any{row}, Requests: 2}, nil
}
