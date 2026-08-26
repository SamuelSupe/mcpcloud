package providers

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mcpcloud/internal/provider"
)

type gcpSQLDetailResponse struct {
	Name             string `json:"name"`
	Project          string `json:"project"`
	Region           string `json:"region"`
	DatabaseVersion  string `json:"databaseVersion"`
	State            string `json:"state"`
	GCEZone          string `json:"gceZone"`
	SecondaryGCEZone string `json:"secondaryGceZone"`
	InstanceType     string `json:"instanceType"`
	CreateTime       string `json:"createTime"`
	Settings         struct {
		Tier                      string `json:"tier"`
		AvailabilityType          string `json:"availabilityType"`
		DeletionProtectionEnabled bool   `json:"deletionProtectionEnabled"`
		StorageAutoResize         bool   `json:"storageAutoResize"`
		DataDiskSizeGB            string `json:"dataDiskSizeGb"`
		BackupConfiguration       struct {
			Enabled                     bool `json:"enabled"`
			PointInTimeRecoveryEnabled  bool `json:"pointInTimeRecoveryEnabled"`
			TransactionLogRetentionDays int  `json:"transactionLogRetentionDays"`
			BackupRetentionSettings     struct {
				RetainedBackups int `json:"retainedBackups"`
			} `json:"backupRetentionSettings"`
		} `json:"backupConfiguration"`
		IPConfiguration struct {
			IPv4Enabled    bool   `json:"ipv4Enabled"`
			PrivateNetwork string `json:"privateNetwork"`
			RequireSSL     bool   `json:"requireSsl"`
			SSLMode        string `json:"sslMode"`
		} `json:"ipConfiguration"`
		DatabaseFlags []struct {
			Name string `json:"name"`
		} `json:"databaseFlags"`
	} `json:"settings"`
	DiskEncryptionConfiguration struct {
		KMSKeyName string `json:"kmsKeyName"`
	} `json:"diskEncryptionConfiguration"`
}

type gcpGKEDetailResponse struct {
	Name                 string            `json:"name"`
	SelfLink             string            `json:"selfLink"`
	Location             string            `json:"location"`
	Zone                 string            `json:"zone"`
	Status               string            `json:"status"`
	CreateTime           string            `json:"createTime"`
	CurrentMasterVersion string            `json:"currentMasterVersion"`
	Network              string            `json:"network"`
	Subnetwork           string            `json:"subnetwork"`
	ClusterIPv4CIDR      string            `json:"clusterIpv4Cidr"`
	ServicesIPv4CIDR     string            `json:"servicesIpv4Cidr"`
	ResourceLabels       map[string]string `json:"resourceLabels"`
	Locations            []string          `json:"locations"`
	NetworkConfig        struct {
		Network    string `json:"network"`
		Subnetwork string `json:"subnetwork"`
	} `json:"networkConfig"`
	PrivateClusterConfig struct {
		EnablePrivateNodes    bool `json:"enablePrivateNodes"`
		EnablePrivateEndpoint bool `json:"enablePrivateEndpoint"`
	} `json:"privateClusterConfig"`
	LoggingService string `json:"loggingService"`
	LoggingConfig  struct {
		EnableComponents []string `json:"enableComponents"`
	} `json:"loggingConfig"`
	DatabaseEncryption struct {
		State string `json:"state"`
	} `json:"databaseEncryption"`
	DeletionProtection bool `json:"deletionProtection"`
}

func (a *gcpAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	configured := append([]string(nil), a.profile.Scopes.Projects...)
	if scope := a.profile.Options["asset_scope"]; strings.HasPrefix(scope, "projects/") {
		configured = append(configured, scopeTail(scope))
	}
	project, err := exactDetailScope(configured, projectRaw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	client, err := a.httpClient(ctx)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	if spec.domain == "database" {
		instance, err := nativeIdentifier(request.Params, "instance")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		var detail gcpSQLDetailResponse
		endpoint := "https://sqladmin.googleapis.com/sql/v1beta4/projects/" + url.PathEscape(project) + "/instances/" + url.PathEscape(instance)
		if err := gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, spec.operation, &detail); err != nil {
			return provider.Page{}, err
		}
		if detail.Name == "" {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		return oneDeepDetailPage(a.gcpSQLDetailRow(detail, project)), nil
	}
	location, err := nativeIdentifier(request.Params, "location")
	if err != nil || !cloudRegionPattern.MatchString(location) {
		if err == nil {
			err = &provider.Error{Code: "invalid_parameter", Message: "parameter location contains an invalid identifier"}
		}
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	cluster, err := nativeIdentifier(request.Params, "cluster")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	var detail gcpGKEDetailResponse
	endpoint := "https://container.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location) + "/clusters/" + url.PathEscape(cluster)
	if err := gcpJSONRequest(ctx, client, http.MethodGet, endpoint, nil, spec.operation, &detail); err != nil {
		return provider.Page{}, err
	}
	if detail.Name == "" {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(a.gcpGKEDetailRow(detail, project, location)), nil
}

func (a *gcpAdapter) gcpSQLDetailRow(detail gcpSQLDetailResponse, project string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, detail.Name, detail.Name, "sqladmin", "sqladmin.googleapis.com/Instance", "database", "database", detail.Region, project)
	row["state"] = strings.ToLower(detail.State)
	row["zone"] = detail.GCEZone
	setDetailTime(row, "created_at", detail.CreateTime, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = strings.ToLower(strings.Split(detail.DatabaseVersion, "_")[0])
	attributes["engine_version"] = detail.DatabaseVersion
	attributes["instance_type"] = detail.Settings.Tier
	attributes["storage_gb"] = detail.Settings.DataDiskSizeGB
	attributes["vpc_id"] = lastName(detail.Settings.IPConfiguration.PrivateNetwork)
	setPosture(row, "customer_managed_encryption_key_enabled", detail.DiskEncryptionConfiguration.KMSKeyName != "")
	setPosture(row, "backup_enabled", detail.Settings.BackupConfiguration.Enabled)
	setPosture(row, "backup_retention_days", detail.Settings.BackupConfiguration.TransactionLogRetentionDays)
	setPosture(row, "retained_backups", detail.Settings.BackupConfiguration.BackupRetentionSettings.RetainedBackups)
	setPosture(row, "point_in_time_recovery_enabled", detail.Settings.BackupConfiguration.PointInTimeRecoveryEnabled)
	setPosture(row, "multi_zone", detail.Settings.AvailabilityType == "REGIONAL" || detail.SecondaryGCEZone != "")
	setPosture(row, "publicly_accessible", detail.Settings.IPConfiguration.IPv4Enabled)
	setPosture(row, "private_endpoint_enabled", detail.Settings.IPConfiguration.PrivateNetwork != "")
	setPosture(row, "transport_encryption_required", detail.Settings.IPConfiguration.RequireSSL || detail.Settings.IPConfiguration.SSLMode != "")
	setPosture(row, "deletion_protection", detail.Settings.DeletionProtectionEnabled)
	setPosture(row, "storage_auto_resize", detail.Settings.StorageAutoResize)
	flags := make([]string, 0, len(detail.Settings.DatabaseFlags))
	for _, flag := range detail.Settings.DatabaseFlags {
		flags = append(flags, flag.Name)
	}
	setRelated(row, "database_flag_names", flags)
	row["native"].(map[string]any)["instance_type"] = detail.Settings.Tier
	return row
}

func (a *gcpAdapter) gcpGKEDetailRow(detail gcpGKEDetailResponse, project, location string) map[string]any {
	id := detail.SelfLink
	if id == "" {
		id = "projects/" + project + "/locations/" + location + "/clusters/" + detail.Name
	}
	region := location
	zone := detail.Zone
	if zone != "" {
		region = zone
		if index := strings.LastIndex(zone, "-"); index > 0 {
			region = zone[:index]
		}
	}
	row := newDeepDetailRow(a.Provider(), a.name, id, detail.Name, "container", "container.googleapis.com/Cluster", "kubernetes", "cluster", region, project)
	row["zone"] = zone
	row["state"] = strings.ToLower(detail.Status)
	setDetailTime(row, "created_at", detail.CreateTime, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = detail.CurrentMasterVersion
	attributes["network_plugin"] = "gke"
	attributes["service_cidr"] = detail.ServicesIPv4CIDR
	attributes["pod_cidr"] = detail.ClusterIPv4CIDR
	network, subnet := detail.NetworkConfig.Network, detail.NetworkConfig.Subnetwork
	if network == "" {
		network = detail.Network
	}
	if subnet == "" {
		subnet = detail.Subnetwork
	}
	attributes["vpc_id"] = lastName(network)
	setRelated(row, "subnet_ids", []string{lastName(subnet)})
	setRelated(row, "locations", detail.Locations)
	setPosture(row, "public_endpoint_enabled", !detail.PrivateClusterConfig.EnablePrivateEndpoint)
	setPosture(row, "private_endpoint_enabled", detail.PrivateClusterConfig.EnablePrivateNodes || detail.PrivateClusterConfig.EnablePrivateEndpoint)
	setPosture(row, "control_plane_logging_enabled", detail.LoggingService != "none" || len(detail.LoggingConfig.EnableComponents) > 0)
	setPosture(row, "audit_enabled", stringIn(detail.LoggingConfig.EnableComponents, "APISERVER") || stringIn(detail.LoggingConfig.EnableComponents, "SYSTEM_COMPONENTS"))
	setPosture(row, "secrets_encryption_enabled", strings.EqualFold(detail.DatabaseEncryption.State, "ENCRYPTED"))
	setPosture(row, "deletion_protection", detail.DeletionProtection)
	for key, value := range detail.ResourceLabels {
		row["tags"].(map[string]any)[key] = value
	}
	return row
}
