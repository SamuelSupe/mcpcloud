package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcpcloud/internal/provider"
)

type gcpInstanceDetailResponse struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Status             string            `json:"status"`
	Zone               string            `json:"zone"`
	MachineType        string            `json:"machineType"`
	CreationTimestamp  string            `json:"creationTimestamp"`
	Labels             map[string]string `json:"labels"`
	CPUPlatform        string            `json:"cpuPlatform"`
	DeletionProtection bool              `json:"deletionProtection"`
	CanIPForward       bool              `json:"canIpForward"`
	NetworkInterfaces  []struct {
		Network       string `json:"network"`
		Subnetwork    string `json:"subnetwork"`
		NetworkIP     string `json:"networkIP"`
		IPv6Address   string `json:"ipv6Address"`
		AccessConfigs []struct {
			NatIP string `json:"natIP"`
		} `json:"accessConfigs"`
	} `json:"networkInterfaces"`
}

func (a *gcpAdapter) getInstanceDetail(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
	if err := validateInstanceDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	configuredProjects := append([]string(nil), a.profile.Scopes.Projects...)
	if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
		configuredProjects = append(configuredProjects, scopeTail(scope))
	}
	project, err := exactDetailScope(configuredProjects, projectRaw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	zone, err := nativeIdentifier(request.Params, "zone")
	if err != nil || !cloudRegionPattern.MatchString(zone) {
		if err == nil {
			err = fmt.Errorf("parameter zone contains an invalid identifier")
		}
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	instanceName, err := nativeIdentifier(request.Params, "instance")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	endpoint := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(project) + "/zones/" + url.PathEscape(zone) + "/instances/" + url.PathEscape(instanceName)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_request", Operation: spec.operation, Message: err.Error()}
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "gcp_api_error", Operation: spec.operation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		code := "gcp_api_error"
		if response.StatusCode == http.StatusNotFound {
			code = "not_found"
		}
		return provider.Page{}, &provider.Error{Code: code, Operation: spec.operation, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	var detail gcpInstanceDetailResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&detail); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: err.Error()}
	}
	return provider.Page{Rows: []map[string]any{a.gcpInstanceDetailRow(detail, project, zone)}, Scanned: 1, Requests: 1}, nil
}

func (a *gcpAdapter) gcpInstanceDetailRow(detail gcpInstanceDetailResponse, project, zone string) map[string]any {
	id := detail.ID
	if id == "" {
		id = detail.Name
	}
	region := zone
	if index := strings.LastIndex(zone, "-"); index > 0 {
		region = zone[:index]
	}
	row := newInstanceDetailRow(a.Provider(), a.name, id, detail.Name, "compute", "compute.googleapis.com/Instance", region, project)
	row["zone"] = zone
	row["state"] = strings.ToLower(detail.Status)
	setDetailTime(row, "created_at", detail.CreationTimestamp, time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for key, value := range detail.Labels {
		tags[key] = value
	}
	row["tags"] = tags
	privateIPs, publicIPs := []string{}, []string{}
	vpcID, subnetID := "", ""
	for _, networkInterface := range detail.NetworkInterfaces {
		if vpcID == "" {
			vpcID = lastName(networkInterface.Network)
		}
		if subnetID == "" {
			subnetID = lastName(networkInterface.Subnetwork)
		}
		privateIPs = appendUniqueText(privateIPs, networkInterface.NetworkIP)
		privateIPs = appendUniqueText(privateIPs, networkInterface.IPv6Address)
		for _, access := range networkInterface.AccessConfigs {
			publicIPs = appendUniqueText(publicIPs, access.NatIP)
		}
	}
	instanceType := lastName(detail.MachineType)
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = instanceType
	attributes["vpc_id"] = vpcID
	attributes["subnet_id"] = subnetID
	attributes["private_ip_addresses"] = privateIPs
	attributes["public_ip_addresses"] = publicIPs
	attributes["cpu_platform"] = detail.CPUPlatform
	attributes["deletion_protection"] = detail.DeletionProtection
	attributes["can_ip_forward"] = detail.CanIPForward
	setRelated(row, "network_ids", []string{vpcID})
	setRelated(row, "subnet_ids", []string{subnetID})
	row["native"].(map[string]any)["instance_type"] = instanceType
	return row
}
