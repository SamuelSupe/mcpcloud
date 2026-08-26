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

type azurePostgreSQLDetailResponse struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Location string            `json:"location"`
	Tags     map[string]string `json:"tags"`
	SKU      struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
	} `json:"sku"`
	Properties struct {
		Version          string `json:"version"`
		State            string `json:"state"`
		AvailabilityZone string `json:"availabilityZone"`
		CreateTime       string `json:"createTime"`
		Storage          struct {
			StorageSizeGB int    `json:"storageSizeGB"`
			AutoGrow      string `json:"autoGrow"`
		} `json:"storage"`
		Backup struct {
			BackupRetentionDays int    `json:"backupRetentionDays"`
			GeoRedundantBackup  string `json:"geoRedundantBackup"`
		} `json:"backup"`
		HighAvailability struct {
			Mode                    string `json:"mode"`
			State                   string `json:"state"`
			StandbyAvailabilityZone string `json:"standbyAvailabilityZone"`
		} `json:"highAvailability"`
		DataEncryption struct {
			Type string `json:"type"`
		} `json:"dataEncryption"`
		Network struct {
			PublicNetworkAccess       string `json:"publicNetworkAccess"`
			DelegatedSubnetResourceID string `json:"delegatedSubnetResourceId"`
			PrivateDNSEntryResourceID string `json:"privateDnsZoneArmResourceId"`
		} `json:"network"`
		ReplicationRole string `json:"replicationRole"`
	} `json:"properties"`
	SystemData struct {
		CreatedAt      string `json:"createdAt"`
		LastModifiedAt string `json:"lastModifiedAt"`
	} `json:"systemData"`
}

type azureAKSDetailResponse struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags"`
	Properties struct {
		ProvisioningState        string `json:"provisioningState"`
		KubernetesVersion        string `json:"kubernetesVersion"`
		CurrentKubernetesVersion string `json:"currentKubernetesVersion"`
		EnableRBAC               bool   `json:"enableRBAC"`
		DisableLocalAccounts     bool   `json:"disableLocalAccounts"`
		SupportPlan              string `json:"supportPlan"`
		NetworkProfile           struct {
			NetworkPlugin string `json:"networkPlugin"`
			NetworkPolicy string `json:"networkPolicy"`
			PodCIDR       string `json:"podCidr"`
			ServiceCIDR   string `json:"serviceCidr"`
		} `json:"networkProfile"`
		APIServerAccessProfile struct {
			EnablePrivateCluster           bool `json:"enablePrivateCluster"`
			EnablePrivateClusterPublicFQDN bool `json:"enablePrivateClusterPublicFQDN"`
			DisableRunCommand              bool `json:"disableRunCommand"`
		} `json:"apiServerAccessProfile"`
		SecurityProfile struct {
			Defender struct {
				SecurityMonitoring struct {
					Enabled bool `json:"enabled"`
				} `json:"securityMonitoring"`
			} `json:"defender"`
		} `json:"securityProfile"`
		AddonProfiles map[string]struct {
			Enabled bool `json:"enabled"`
		} `json:"addonProfiles"`
		AgentPoolProfiles []struct {
			Name              string   `json:"name"`
			VnetSubnetID      string   `json:"vnetSubnetID"`
			AvailabilityZones []string `json:"availabilityZones"`
		} `json:"agentPoolProfiles"`
	} `json:"properties"`
	SystemData struct {
		CreatedAt      string `json:"createdAt"`
		LastModifiedAt string `json:"lastModifiedAt"`
	} `json:"systemData"`
}

func (a *azureAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	subscriptionRaw, err := nativeIdentifier(request.Params, "subscription_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	subscription, err := exactDetailScope(a.profile.Scopes.Subscriptions, subscriptionRaw, spec.operation, "subscription_id")
	if err != nil {
		return provider.Page{}, err
	}
	resourceGroup, err := nativeLiteral(request.Params, "resource_group")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	name, err := nativeLiteral(request.Params, "name")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	token, err := a.accessToken(ctx, spec.operation)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	base := "https://management.azure.com/subscriptions/" + url.PathEscape(subscription) + "/resourceGroups/" + url.PathEscape(resourceGroup) + "/providers/"
	if spec.domain == "database" {
		var detail azurePostgreSQLDetailResponse
		endpoint := base + "Microsoft.DBforPostgreSQL/flexibleServers/" + url.PathEscape(name) + "?api-version=2025-08-01"
		if err := azureDetailGET(ctx, endpoint, token, spec.operation, &detail); err != nil {
			return provider.Page{}, err
		}
		if detail.ID == "" {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		return oneDeepDetailPage(a.azurePostgreSQLDetailRow(detail, subscription, resourceGroup)), nil
	}
	var detail azureAKSDetailResponse
	endpoint := base + "Microsoft.ContainerService/managedClusters/" + url.PathEscape(name) + "?api-version=2025-10-01"
	if err := azureDetailGET(ctx, endpoint, token, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if detail.ID == "" {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(a.azureAKSDetailRow(detail, subscription, resourceGroup)), nil
}

func azureDetailGET(ctx context.Context, endpoint, token, operation string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &provider.Error{Code: "invalid_request", Operation: operation, Message: err.Error()}
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return &provider.Error{Code: "azure_api_error", Operation: operation, Message: err.Error(), Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		code := "azure_api_error"
		if response.StatusCode == http.StatusNotFound {
			code = "not_found"
		}
		return &provider.Error{Code: code, Operation: operation, Message: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body))), Retryable: response.StatusCode == 429 || response.StatusCode >= 500}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(target); err != nil {
		return &provider.Error{Code: "invalid_provider_response", Operation: operation, Message: err.Error()}
	}
	return nil
}

func (a *azureAdapter) azurePostgreSQLDetailRow(detail azurePostgreSQLDetailResponse, subscription, resourceGroup string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, detail.ID, detail.Name, "dbforpostgresql", "Microsoft.DBforPostgreSQL/flexibleServers", "database", "database", detail.Location, subscription)
	row["state"] = strings.ToLower(detail.Properties.State)
	row["zone"] = detail.Properties.AvailabilityZone
	row["tags"] = stringTags(detail.Tags)
	row["scope"].(map[string]any)["resource_group_id"] = resourceGroup
	created := detail.SystemData.CreatedAt
	if created == "" {
		created = detail.Properties.CreateTime
	}
	setDetailTime(row, "created_at", created, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", detail.SystemData.LastModifiedAt, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = "postgresql"
	attributes["engine_version"] = detail.Properties.Version
	attributes["instance_type"] = detail.SKU.Name
	attributes["storage_gb"] = detail.Properties.Storage.StorageSizeGB
	attributes["billing_mode"] = detail.SKU.Tier
	attributes["subnet_id"] = detail.Properties.Network.DelegatedSubnetResourceID
	setPosture(row, "encryption_enabled", detail.Properties.DataEncryption.Type != "")
	setPosture(row, "backup_enabled", detail.Properties.Backup.BackupRetentionDays > 0)
	setPosture(row, "backup_retention_days", detail.Properties.Backup.BackupRetentionDays)
	setPosture(row, "geo_redundant_backup_enabled", strings.EqualFold(detail.Properties.Backup.GeoRedundantBackup, "Enabled"))
	setPosture(row, "multi_zone", strings.Contains(strings.ToLower(detail.Properties.HighAvailability.Mode), "zone") || detail.Properties.HighAvailability.StandbyAvailabilityZone != "")
	setPosture(row, "publicly_accessible", strings.EqualFold(detail.Properties.Network.PublicNetworkAccess, "Enabled"))
	setPosture(row, "private_endpoint_enabled", detail.Properties.Network.DelegatedSubnetResourceID != "" || detail.Properties.Network.PrivateDNSEntryResourceID != "")
	setPosture(row, "storage_auto_resize", strings.EqualFold(detail.Properties.Storage.AutoGrow, "Enabled"))
	row["native"].(map[string]any)["instance_type"] = detail.SKU.Name
	return row
}

func (a *azureAdapter) azureAKSDetailRow(detail azureAKSDetailResponse, subscription, resourceGroup string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, detail.ID, detail.Name, "containerservice", "Microsoft.ContainerService/managedClusters", "kubernetes", "cluster", detail.Location, subscription)
	row["state"] = strings.ToLower(detail.Properties.ProvisioningState)
	row["tags"] = stringTags(detail.Tags)
	row["scope"].(map[string]any)["resource_group_id"] = resourceGroup
	setDetailTime(row, "created_at", detail.SystemData.CreatedAt, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", detail.SystemData.LastModifiedAt, time.RFC3339, time.RFC3339Nano)
	version := detail.Properties.CurrentKubernetesVersion
	if version == "" {
		version = detail.Properties.KubernetesVersion
	}
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = version
	attributes["network_plugin"] = detail.Properties.NetworkProfile.NetworkPlugin
	attributes["network_policy"] = detail.Properties.NetworkProfile.NetworkPolicy
	attributes["service_cidr"] = detail.Properties.NetworkProfile.ServiceCIDR
	attributes["pod_cidr"] = detail.Properties.NetworkProfile.PodCIDR
	attributes["support_type"] = detail.Properties.SupportPlan
	subnets, pools, zones := []string{}, []string{}, []string{}
	for _, pool := range detail.Properties.AgentPoolProfiles {
		pools = append(pools, pool.Name)
		subnets = append(subnets, pool.VnetSubnetID)
		zones = append(zones, pool.AvailabilityZones...)
	}
	setRelated(row, "node_pool_ids", pools)
	setRelated(row, "subnet_ids", subnets)
	setRelated(row, "availability_zones", zones)
	private := detail.Properties.APIServerAccessProfile.EnablePrivateCluster
	setPosture(row, "private_endpoint_enabled", private)
	setPosture(row, "public_endpoint_enabled", !private || detail.Properties.APIServerAccessProfile.EnablePrivateClusterPublicFQDN)
	setPosture(row, "local_accounts_disabled", detail.Properties.DisableLocalAccounts)
	setPosture(row, "rbac_enabled", detail.Properties.EnableRBAC)
	setPosture(row, "run_command_disabled", detail.Properties.APIServerAccessProfile.DisableRunCommand)
	setPosture(row, "security_monitoring_enabled", detail.Properties.SecurityProfile.Defender.SecurityMonitoring.Enabled)
	return row
}

func stringTags(values map[string]string) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
