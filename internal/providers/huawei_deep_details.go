package providers

import (
	"context"
	"strconv"
	"strings"
	"time"

	cce "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3"
	ccemodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	cceregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/region"
	rds "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rds/v3"
	rdsmodel "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rds/v3/model"
	rdsregion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/rds/v3/region"

	"mcpcloud/internal/provider"
)

type huaweiRDSDetailAPI interface {
	ListInstances(*rdsmodel.ListInstancesRequest) (*rdsmodel.ListInstancesResponse, error)
}

type huaweiCCEDetailAPI interface {
	ShowCluster(*ccemodel.ShowClusterRequest) (*ccemodel.ShowClusterResponse, error)
}

var callHuaweiRDSDetail = func(client huaweiRDSDetailAPI, request *rdsmodel.ListInstancesRequest) (*rdsmodel.ListInstancesResponse, error) {
	return client.ListInstances(request)
}

var callHuaweiCCEDetail = func(client huaweiCCEDetailAPI, request *ccemodel.ShowClusterRequest) (*ccemodel.ShowClusterResponse, error) {
	return client.ShowCluster(request)
}

func (a *huaweiAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, projectRaw, spec.operation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	regionID, err := exactDetailRegion(request.Region, a.profile.Regions, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	credential, err := a.huaweiBasicCredential(spec.operation, project)
	if err != nil {
		return provider.Page{}, err
	}
	if spec.domain == "database" {
		instanceID, err := nativeIdentifier(request.Params, "instance_id")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		region, err := rdsregion.SafeValueOf(regionID)
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: spec.operation, Message: err.Error()}
		}
		hc, err := rds.RdsClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
		if err != nil {
			return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: spec.operation, Message: err.Error()}
		}
		var client huaweiRDSDetailAPI = rds.NewRdsClient(hc)
		limit := int32(1)
		response, err := callHuaweiRDSDetail(client, &rdsmodel.ListInstancesRequest{Id: &instanceID, Limit: &limit})
		if err != nil {
			return provider.Page{}, huaweiDeepError(spec.operation, err)
		}
		if response != nil && response.Instances != nil {
			for _, instance := range *response.Instances {
				if instance.Id == instanceID {
					return oneDeepDetailPage(a.huaweiRDSDetailRow(instance, project)), nil
				}
			}
		}
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	clusterID, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	region, err := cceregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: spec.operation, Message: err.Error()}
	}
	hc, err := cce.CceClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: spec.operation, Message: err.Error()}
	}
	var client huaweiCCEDetailAPI = cce.NewCceClient(hc)
	response, err := callHuaweiCCEDetail(client, &ccemodel.ShowClusterRequest{ClusterId: clusterID})
	if err != nil {
		return provider.Page{}, huaweiDeepError(spec.operation, err)
	}
	if response == nil || response.Metadata == nil || response.Metadata.Uid == nil || *response.Metadata.Uid == "" {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(a.huaweiCCEDetailRow(response, regionID, project)), nil
}

func (a *huaweiAdapter) listCCENodes(ctx context.Context, request provider.NativeRequest) (provider.Page, error) {
	operationSpec := provider.Operation{Name: huaweiCCENodesOperation, Parameters: detailParameters("project_id", "cluster_id")}
	if err := operationSpec.ValidateParams(request.Params); err != nil {
		return provider.Page{}, err
	}
	projectRaw, err := nativeIdentifier(request.Params, "project_id")
	if err != nil {
		return provider.Page{}, deepParameterError(huaweiCCENodesOperation, err)
	}
	project, err := exactDetailScope(a.profile.Scopes.Projects, projectRaw, huaweiCCENodesOperation, "project_id")
	if err != nil {
		return provider.Page{}, err
	}
	clusterID, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil || clusterID == "" {
		return provider.Page{}, &provider.Error{Code: "missing_parameter", Operation: huaweiCCENodesOperation, Message: "parameter cluster_id is required"}
	}
	regionID, err := exactDetailRegion(request.Region, a.profile.Regions, huaweiCCENodesOperation)
	if err != nil {
		return provider.Page{}, err
	}
	credential, err := a.huaweiBasicCredential(huaweiCCENodesOperation, project)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := cceregion.SafeValueOf(regionID)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_region", Operation: huaweiCCENodesOperation, Message: err.Error()}
	}
	hc, err := cce.CceClientBuilder().WithRegion(region).WithCredential(credential).WithHttpConfig(huaweiHTTPConfig(ctx)).SafeBuild()
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "authentication_error", Operation: huaweiCCENodesOperation, Message: err.Error()}
	}
	limit := request.Limit
	if limit <= 0 || limit > 2000 {
		limit = 100
	}
	limit32 := int32(limit)
	apiRequest := &ccemodel.ListNodesRequest{ClusterId: clusterID, Limit: &limit32}
	if request.PageToken != "" {
		apiRequest.Marker = &request.PageToken
	}
	response, err := cce.NewCceClient(hc).ListNodes(apiRequest)
	if err != nil {
		return provider.Page{}, huaweiDeepError(huaweiCCENodesOperation, err)
	}
	rows := []map[string]any{}
	if response != nil && response.Items != nil {
		for _, node := range *response.Items {
			if node.Metadata == nil || node.Metadata.Uid == nil {
				continue
			}
			name := ""
			if node.Metadata.Name != nil { name = *node.Metadata.Name }
			row := newDeepDetailRow(a.Provider(), a.name, *node.Metadata.Uid, name, "cce", "CCE::Node", "kubernetes", "node", regionID, project)
			attrs := row["attributes"].(map[string]any)
			if node.Metadata.OwnerReferences != nil {
				if node.Metadata.OwnerReferences.NodepoolID != nil { attrs["node_pool_id"] = *node.Metadata.OwnerReferences.NodepoolID }
				if node.Metadata.OwnerReferences.NodepoolName != nil { attrs["node_pool_name"] = *node.Metadata.OwnerReferences.NodepoolName }
			}
			if node.Status != nil {
				if node.Status.Phase != nil { row["state"] = strings.ToLower(node.Status.Phase.Value()) }
				if node.Status.ServerId != nil { attrs["server_id"] = *node.Status.ServerId }
				if node.Status.ConfigurationUpToDate != nil { attrs["configuration_up_to_date"] = *node.Status.ConfigurationUpToDate }
			}
			rows = append(rows, row)
		}
	}
	next := ""
	if response != nil && response.PageInfo != nil && response.PageInfo.NextMarker != nil { next = *response.PageInfo.NextMarker }
	return provider.Page{Rows: rows, NextToken: next, Scanned: len(rows), Requests: 1}, nil
}

func huaweiDeepError(operation string, err error) error {
	return &provider.Error{Code: "huawei_api_error", Operation: operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
}

func (a *huaweiAdapter) huaweiRDSDetailRow(instance rdsmodel.InstanceResponse, project string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, instance.Id, instance.Name, "rds", "RDS::INSTANCE", "database", "database", instance.Region, project)
	row["state"] = strings.ToLower(instance.Status)
	setDetailTime(row, "created_at", instance.Created, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", instance.Updated, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	if instance.Datastore != nil {
		attributes["engine"] = strings.ToLower(instance.Datastore.Type.Value())
		attributes["engine_version"] = instance.Datastore.Version
		if instance.Datastore.CompleteVersion != nil {
			attributes["engine_complete_version"] = *instance.Datastore.CompleteVersion
		}
	}
	attributes["instance_type"] = instance.FlavorRef
	if instance.Cpu != nil {
		attributes["cpu_count"], _ = strconv.Atoi(*instance.Cpu)
	}
	if instance.Mem != nil {
		attributes["memory_gb"], _ = strconv.Atoi(*instance.Mem)
	}
	if instance.Volume != nil {
		attributes["storage_gb"] = instance.Volume.Size
		attributes["storage_type"] = instance.Volume.Type.Value()
	}
	attributes["vpc_id"] = instance.VpcId
	attributes["subnet_id"] = instance.SubnetId
	if instance.ChargeInfo != nil {
		attributes["billing_mode"] = instance.ChargeInfo.ChargeMode.Value()
	}
	setRelated(row, "security_group_ids", []string{instance.SecurityGroupId})
	related := make([]string, 0, len(instance.RelatedInstance))
	for _, item := range instance.RelatedInstance {
		related = append(related, item.Id)
	}
	setRelated(row, "replica_ids", related)
	setPosture(row, "encryption_enabled", instance.DiskEncryptionId != "")
	setPosture(row, "transport_encryption_required", instance.EnableSsl)
	setPosture(row, "multi_zone", strings.EqualFold(instance.Type, "Ha"))
	setPosture(row, "publicly_accessible", len(instance.PublicIps) > 0 || (instance.PublicDnsNames != nil && len(*instance.PublicDnsNames) > 0))
	if instance.BackupStrategy != nil {
		setPosture(row, "backup_enabled", instance.BackupStrategy.KeepDays > 0)
		setPosture(row, "backup_retention_days", instance.BackupStrategy.KeepDays)
	}
	row["native"].(map[string]any)["instance_type"] = instance.FlavorRef
	return row
}

func (a *huaweiAdapter) huaweiCCEDetailRow(response *ccemodel.ShowClusterResponse, region, project string) map[string]any {
	metadata := response.Metadata
	name := metadata.Name
	if metadata.Alias != nil && *metadata.Alias != "" {
		name = *metadata.Alias
	}
	row := newDeepDetailRow(a.Provider(), a.name, *metadata.Uid, name, "cce", "CCE::Cluster", "kubernetes", "cluster", region, project)
	row["tags"] = stringTags(metadata.Labels)
	if metadata.CreationTimestamp != nil {
		setDetailTime(row, "created_at", *metadata.CreationTimestamp, time.RFC3339, time.RFC3339Nano)
	}
	if metadata.UpdateTimestamp != nil {
		setDetailTime(row, "updated_at", *metadata.UpdateTimestamp, time.RFC3339, time.RFC3339Nano)
	}
	if response.Status != nil && response.Status.Phase != nil {
		row["state"] = strings.ToLower(*response.Status.Phase)
	}
	if response.Spec == nil {
		return row
	}
	spec := response.Spec
	attributes := row["attributes"].(map[string]any)
	if spec.Version != nil {
		attributes["version"] = *spec.Version
	}
	if spec.PlatformVersion != nil {
		attributes["platform_version"] = *spec.PlatformVersion
	}
	if spec.Category != nil {
		attributes["cluster_type"] = spec.Category.Value()
	}
	if spec.Flavor != nil {
		attributes["instance_type"] = *spec.Flavor
	}
	if spec.HostNetwork != nil {
		attributes["vpc_id"] = spec.HostNetwork.Vpc
		setRelated(row, "subnet_ids", []string{spec.HostNetwork.Subnet})
		groups := []string{}
		if spec.HostNetwork.SecurityGroup != nil {
			groups = append(groups, *spec.HostNetwork.SecurityGroup)
		}
		if spec.HostNetwork.ControlPlaneSecurityGroup != nil {
			groups = append(groups, *spec.HostNetwork.ControlPlaneSecurityGroup)
		}
		setRelated(row, "security_group_ids", groups)
	}
	if spec.ContainerNetwork != nil {
		attributes["network_plugin"] = spec.ContainerNetwork.Mode.Value()
		if spec.ContainerNetwork.Cidr != nil {
			attributes["pod_cidr"] = *spec.ContainerNetwork.Cidr
		}
	}
	if spec.ServiceNetwork != nil && spec.ServiceNetwork.IPv4CIDR != nil {
		attributes["service_cidr"] = *spec.ServiceNetwork.IPv4CIDR
	} else if spec.KubernetesSvcIpRange != nil {
		attributes["service_cidr"] = *spec.KubernetesSvcIpRange
	}
	setPosture(row, "public_endpoint_enabled", spec.PublicAccess != nil)
	setPosture(row, "private_endpoint_enabled", true)
	setPosture(row, "secrets_encryption_enabled", spec.EncryptionConfig != nil)
	setPosture(row, "control_plane_storage_encrypted", spec.EnableMasterVolumeEncryption != nil && *spec.EnableMasterVolumeEncryption)
	setPosture(row, "deletion_protection", spec.DeletionProtection != nil && *spec.DeletionProtection)
	return row
}
