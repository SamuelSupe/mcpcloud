package providers

import (
	"context"
	"fmt"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"net/url"
	"time"
)

func init() {
	deepDetailCatalog[model.ProviderAzure] = append(deepDetailCatalog[model.ProviderAzure], deepDetailSpec{operation: "azure.containerservice.agent_pools.list", service: "containerservice", domain: "node_pool", kind: "node_pool", description: "Read AKS node pool configuration, not live Kubernetes nodes", parameters: detailParameters("subscription_id", "resource_group", "cluster"), required: []string{"subscription_id", "resource_group", "cluster"}})
	deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: "gcp.container.node_pools.list", service: "container", domain: "node_pool", kind: "node_pool", description: "Read GKE node pool configuration, not live Kubernetes nodes", parameters: detailParameters("project_id", "location", "cluster"), required: []string{"project_id", "location", "cluster"}})
}

type poolListResponse struct {
	Value     []map[string]any `json:"value"`
	NodePools []map[string]any `json:"nodePools"`
	NextLink  string           `json:"nextLink"`
}

func poolRows(cloud model.Provider, profile, scope, region, cluster string, pools []map[string]any) provider.Page {
	page := provider.Page{Requests: 1}
	for _, p := range pools {
		props := p
		if nested, ok := p["properties"].(map[string]any); ok {
			props = nested
		}
		id := stringValue(p["id"])
		if id == "" {
			id = stringValue(p["selfLink"])
		}
		row := baseRow(cloud, profile, id, stringValue(p["name"]), "kubernetes", "node_pool", region, scope, time.Now())
		row["domain"] = "kubernetes"
		row["kind"] = "node_pool"
		attrs := row["attributes"].(map[string]any)
		attrs["cluster"] = cluster
		attrs["data_source"] = "cloud_control_plane_configuration"
		for _, key := range []string{"count", "initialNodeCount", "currentNodeCount", "vmSize", "orchestratorVersion", "version", "provisioningState", "status", "mode", "osType", "enableAutoScaling", "minCount", "maxCount", "instanceGroupUrls"} {
			if value, ok := props[key]; ok {
				attrs[key] = value
			}
		}
		if cfg, ok := props["config"].(map[string]any); ok {
			attrs["machine_type"] = cfg["machineType"]
			attrs["disk_size_gb"] = cfg["diskSizeGb"]
		}
		page.Rows = append(page.Rows, row)
	}
	return page
}

func (a *azureAdapter) readNodePools(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	sub, err := nativeIdentifier(req.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	sub, err = exactDetailScope(a.profile.Scopes.Subscriptions, sub, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	rg, err := nativeIdentifier(req.Params, "resource_group")
	if err != nil {
		return provider.Page{}, err
	}
	cluster, err := nativeIdentifier(req.Params, "cluster")
	if err != nil {
		return provider.Page{}, err
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(sub) + "/resourceGroups/" + url.PathEscape(rg) + "/providers/Microsoft.ContainerService/managedClusters/" + url.PathEscape(cluster) + "/agentPools?api-version=2025-10-01"
	var detail poolListResponse
	if err = azureDetailGET(ctx, endpoint, token, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	pools := detail.Value
	requests := 1
	first, _ := url.Parse(endpoint)
	seen := map[string]bool{}
	for detail.NextLink != "" {
		next, err := url.Parse(detail.NextLink)
		if err != nil || next.Scheme != "https" || next.Host != first.Host || next.Path != first.Path || next.User != nil || next.Fragment != "" || requests >= 20 || seen[detail.NextLink] {
			return provider.Page{}, fmt.Errorf("invalid or excessive node pool continuation")
		}
		seen[detail.NextLink] = true
		var more poolListResponse
		if err = azureDetailGET(ctx, detail.NextLink, token, spec.operation, &more); err != nil {
			return provider.Page{}, err
		}
		requests++
		pools = append(pools, more.Value...)
		detail = more
	}
	page := poolRows(a.Provider(), a.name, sub, req.Region, cluster, pools)
	page.Requests = requests
	return page, nil
}

func (a *gcpAdapter) readNodePools(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	project, err := nativeIdentifier(req.Params, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	project, err = exactDetailScope(a.profile.Scopes.Projects, project, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	location, err := nativeIdentifier(req.Params, "location")
	if err != nil {
		return provider.Page{}, err
	}
	cluster, err := nativeIdentifier(req.Params, "cluster")
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	endpoint := "https://container.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location) + "/clusters/" + url.PathEscape(cluster) + "/nodePools"
	var detail poolListResponse
	if err = gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	return poolRows(a.Provider(), a.name, project, gcpRegionForLocation(location), cluster, detail.NodePools), nil
}
