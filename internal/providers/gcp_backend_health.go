package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func init() {
	for _, global := range []bool{false, true} {
		op := "gcp.compute.backend_services.get_health"
		keys := []string{"project_id", "name", "location"}
		if global {
			op = "gcp.compute.global_backend_services.get_health"
			keys = []string{"project_id", "name"}
		}
		deepDetailCatalog[model.ProviderGCP] = append(deepDetailCatalog[model.ProviderGCP], deepDetailSpec{operation: op, service: "compute", domain: "backend_health", kind: "backend_health", description: "Read latest backend health for configured groups via fixed getHealth API; no mutations or annotations", parameters: detailParameters(keys...), required: keys})
	}
}

// References originate from the selected backend service, never caller URLs.
// Validate all references before making any health requests.
func gcpBackendGroupValid(raw, project string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Host != "www.googleapis.com" && u.Host != "compute.googleapis.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 8 || parts[0] != "compute" || parts[1] != "v1" || parts[2] != "projects" || parts[3] != project || (parts[4] != "zones" && parts[4] != "regions") || !cloudRegionPattern.MatchString(parts[5]) || (parts[6] != "instanceGroups" && parts[6] != "networkEndpointGroups") {
		return false
	}
	return validGCPIdentifier(parts[7]) && parts[7] != "." && parts[7] != ".."
}

func (a *gcpAdapter) readBackendHealth(ctx context.Context, req provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, req); err != nil {
		return provider.Page{}, err
	}
	raw, err := nativeIdentifier(req.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	configured := append([]string(nil), a.profile.Scopes.Projects...)
	if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
		configured = append(configured, scopeTail(scope))
	}
	project, err := exactDetailScope(configured, raw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	name, err := nativeIdentifier(req.Params, "name")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	region, path := "global", "global/backendServices"
	if spec.operation == "gcp.compute.backend_services.get_health" {
		region, err = nativeIdentifier(req.Params, "location")
		if err != nil || !cloudRegionPattern.MatchString(region) {
			return provider.Page{}, deepParameterError(spec.operation, fmt.Errorf("invalid location"))
		}
		path = "regions/" + url.PathEscape(region) + "/backendServices"
	}
	endpoint := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(project) + "/" + path + "/" + url.PathEscape(name)
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, err
	}
	closedClient := *client
	closedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var backend struct {
		Name     string `json:"name"`
		Backends []struct {
			Group string `json:"group"`
		} `json:"backends"`
	}
	if err := gcpJSONRequest(ctx, &closedClient, http.MethodGet, endpoint, nil, spec.operation, &backend); err != nil {
		return provider.Page{}, err
	}
	if backend.Name != name {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: "backend service identity mismatch"}
	}
	if len(backend.Backends) > 100 {
		return provider.Page{}, &provider.Error{Code: "capability_unavailable", Operation: spec.operation, Message: "backend health supports at most 100 configured groups per request"}
	}
	for _, group := range backend.Backends {
		if !gcpBackendGroupValid(group.Group, project) {
			return provider.Page{}, &provider.Error{Code: "invalid_scope", Operation: spec.operation, Message: "backend group is outside configured project or unsupported group type"}
		}
	}
	page := provider.Page{Requests: 1}
	seen := map[string]bool{}
	for _, group := range backend.Backends {
		if seen[group.Group] {
			continue
		}
		seen[group.Group] = true
		body, _ := json.Marshal(map[string]string{"group": group.Group})
		var health struct {
			Status []struct {
				Instance    string `json:"instance"`
				IPAddress   string `json:"ipAddress"`
				Port        int    `json:"port"`
				HealthState string `json:"healthState"`
			} `json:"healthStatus"`
		}
		if err := gcpJSONRequest(ctx, &closedClient, http.MethodPost, endpoint+"/getHealth", body, spec.operation, &health); err != nil {
			return provider.Page{}, err
		}
		page.Requests++
		for i, status := range health.Status {
			id := fmt.Sprintf("%s/groups/%s/endpoints/%d", endpoint, url.PathEscape(group.Group), i)
			row := baseRow(a.Provider(), a.name, id, lastName(status.Instance), "compute", "compute#backendServiceGroupHealth", region, project, time.Now().UTC())
			row["domain"], row["kind"] = "network", "backend_health"
			row["attributes"] = map[string]any{"backend_service": name, "group": group.Group, "instance": status.Instance, "ip_address": status.IPAddress, "port": status.Port, "health_state": status.HealthState, "data_source": "compute_getHealth"}
			page.Rows = append(page.Rows, row)
		}
	}
	page.Scanned = len(page.Rows)
	return page, nil
}
