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

type azureVMDetailResponse struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Zones      []string          `json:"zones"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
		TimeCreated       string `json:"timeCreated"`
		LicenseType       string `json:"licenseType"`
		Priority          string `json:"priority"`
		EvictionPolicy    string `json:"evictionPolicy"`
		HardwareProfile   struct {
			VMSize string `json:"vmSize"`
		} `json:"hardwareProfile"`
		StorageProfile struct {
			ImageReference struct {
				ID        string `json:"id"`
				Publisher string `json:"publisher"`
				Offer     string `json:"offer"`
				SKU       string `json:"sku"`
				Version   string `json:"version"`
			} `json:"imageReference"`
			OSDisk struct {
				OSType string `json:"osType"`
			} `json:"osDisk"`
		} `json:"storageProfile"`
		NetworkProfile struct {
			NetworkInterfaces []struct {
				ID string `json:"id"`
			} `json:"networkInterfaces"`
		} `json:"networkProfile"`
	} `json:"properties"`
	SystemData struct {
		CreatedAt      string `json:"createdAt"`
		LastModifiedAt string `json:"lastModifiedAt"`
	} `json:"systemData"`
}

func (a *azureAdapter) getVirtualMachineDetail(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
	if err := validateInstanceDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	subscriptionRaw, err := nativeIdentifier(request.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	subscription, err := exactDetailScope(a.profile.Scopes.Subscriptions, subscriptionRaw, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	resourceGroup, err := nativeLiteral(request.Params, "resource_group")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	name, err := nativeLiteral(request.Params, "name")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	endpoint := "https://management.azure.com/subscriptions/" + url.PathEscape(subscription) + "/resourceGroups/" + url.PathEscape(resourceGroup) + "/providers/Microsoft.Compute/virtualMachines/" + url.PathEscape(name) + "?api-version=2024-03-01"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_request", Operation: spec.operation, Message: err.Error()}
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "azure_api_error", Operation: spec.operation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		code := "azure_api_error"
		if response.StatusCode == http.StatusNotFound {
			code = "not_found"
		}
		return provider.Page{}, &provider.Error{Code: code, Operation: spec.operation, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	var detail azureVMDetailResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&detail); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: err.Error()}
	}
	return provider.Page{Rows: []map[string]any{a.azureVMDetailRow(detail, subscription, resourceGroup)}, Scanned: 1, Requests: 1}, nil
}

func (a *azureAdapter) azureVMDetailRow(detail azureVMDetailResponse, subscription, resourceGroup string) map[string]any {
	row := newInstanceDetailRow(a.Provider(), a.name, detail.ID, detail.Name, "compute", "Microsoft.Compute/virtualMachines", detail.Location, subscription)
	if len(detail.Zones) > 0 {
		row["zone"] = detail.Zones[0]
	}
	row["state"] = strings.ToLower(detail.Properties.ProvisioningState)
	tags := map[string]any{}
	for key, value := range detail.Tags {
		tags[key] = value
	}
	row["tags"] = tags
	scope := row["scope"].(map[string]any)
	scope["resource_group_id"] = resourceGroup
	created := detail.SystemData.CreatedAt
	if created == "" {
		created = detail.Properties.TimeCreated
	}
	setDetailTime(row, "created_at", created, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", detail.SystemData.LastModifiedAt, time.RFC3339, time.RFC3339Nano)
	image := detail.Properties.StorageProfile.ImageReference.ID
	if image == "" {
		parts := []string{detail.Properties.StorageProfile.ImageReference.Publisher, detail.Properties.StorageProfile.ImageReference.Offer, detail.Properties.StorageProfile.ImageReference.SKU, detail.Properties.StorageProfile.ImageReference.Version}
		image = strings.Trim(strings.Join(parts, "/"), "/")
	}
	networkInterfaces := make([]string, 0, len(detail.Properties.NetworkProfile.NetworkInterfaces))
	for _, item := range detail.Properties.NetworkProfile.NetworkInterfaces {
		networkInterfaces = appendUniqueText(networkInterfaces, item.ID)
	}
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = detail.Properties.HardwareProfile.VMSize
	attributes["image_id"] = image
	attributes["os_type"] = detail.Properties.StorageProfile.OSDisk.OSType
	attributes["network_interface_ids"] = networkInterfaces
	attributes["license_type"] = detail.Properties.LicenseType
	attributes["billing_mode"] = detail.Properties.Priority
	attributes["eviction_policy"] = detail.Properties.EvictionPolicy
	setRelated(row, "network_interface_ids", networkInterfaces)
	row["native"].(map[string]any)["instance_type"] = detail.Properties.HardwareProfile.VMSize
	return row
}
