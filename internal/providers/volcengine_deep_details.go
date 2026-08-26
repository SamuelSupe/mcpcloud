package providers

import (
	"context"
	"strings"
	"time"

	"github.com/volcengine/volcengine-go-sdk/service/rdsmysqlv2"
	"github.com/volcengine/volcengine-go-sdk/service/vke"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/provider"
)

type volcengineRDSDetailAPI interface {
	DescribeDBInstanceDetailWithContext(volc.Context, *rdsmysqlv2.DescribeDBInstanceDetailInput, ...request.Option) (*rdsmysqlv2.DescribeDBInstanceDetailOutput, error)
}

type volcengineVKEDetailAPI interface {
	ListClustersWithContext(volc.Context, *vke.ListClustersInput, ...request.Option) (*vke.ListClustersOutput, error)
}

var newVolcengineRDSDetailClient = func(sess *session.Session) volcengineRDSDetailAPI { return rdsmysqlv2.New(sess) }
var newVolcengineVKEDetailClient = func(sess *session.Session) volcengineVKEDetailAPI { return vke.New(sess) }

func (a *volcengineAdapter) readDeepDetail(ctx context.Context, nativeRequest provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := validateDeepDetailRequest(spec, nativeRequest); err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(nativeRequest.Region, a.profile.Regions, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	sess, err := a.session(region, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	if spec.domain == "database" {
		id, err := nativeIdentifier(nativeRequest.Params, "instance_id")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		output, err := newVolcengineRDSDetailClient(sess).DescribeDBInstanceDetailWithContext(ctx, &rdsmysqlv2.DescribeDBInstanceDetailInput{InstanceId: volc.String(id)})
		if err != nil {
			return provider.Page{}, volcengineDeepError(spec.operation, err)
		}
		if output == nil || output.BasicInfo == nil || volc.StringValue(output.BasicInfo.InstanceId) != id {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		return oneDeepDetailPage(a.volcengineRDSDetailRow(output, region, account)), nil
	}
	id, err := nativeIdentifier(nativeRequest.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	pageNumber, pageSize := int32(1), int32(1)
	output, err := newVolcengineVKEDetailClient(sess).ListClustersWithContext(ctx, &vke.ListClustersInput{
		Filter:     &vke.FilterForListClustersInput{Ids: []*string{volc.String(id)}},
		PageNumber: &pageNumber,
		PageSize:   &pageSize,
	})
	if err != nil {
		return provider.Page{}, volcengineDeepError(spec.operation, err)
	}
	for _, cluster := range output.Items {
		if cluster != nil && volc.StringValue(cluster.Id) == id {
			return oneDeepDetailPage(a.volcengineVKEDetailRow(cluster, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
}

func volcengineDeepError(operation string, err error) error {
	return &provider.Error{Code: "volcengine_api_error", Operation: operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
}

func (a *volcengineAdapter) volcengineRDSDetailRow(output *rdsmysqlv2.DescribeDBInstanceDetailOutput, fallbackRegion, account string) map[string]any {
	detail := output.BasicInfo
	region := volc.StringValue(detail.RegionId)
	if region == "" {
		region = fallbackRegion
	}
	row := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(detail.InstanceId), volc.StringValue(detail.InstanceName), "rdsmysql", "RDSMySQL::DBInstance", "database", "database", region, account)
	row["zone"] = volc.StringValue(detail.ZoneId)
	row["state"] = strings.ToLower(volc.StringValue(detail.InstanceStatus))
	setDetailTime(row, "created_at", volc.StringValue(detail.CreateTime), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", volc.StringValue(detail.UpdateTime), time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, tag := range detail.Tags {
		if tag != nil && tag.Key != nil {
			tags[volc.StringValue(tag.Key)] = volc.StringValue(tag.Value)
		}
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = strings.ToLower(volc.StringValue(detail.EngineType))
	attributes["engine_version"] = volc.StringValue(detail.DBEngineVersion)
	attributes["instance_type"] = volc.StringValue(detail.NodeSpec)
	attributes["cpu_count"] = int(volc.Int32Value(detail.VCPU))
	attributes["memory_mb"] = int(volc.Int32Value(detail.Memory))
	attributes["storage_gb"] = volc.Int64Value(detail.StorageSpace)
	attributes["storage_type"] = volc.StringValue(detail.StorageType)
	attributes["vpc_id"] = volc.StringValue(detail.VpcId)
	attributes["subnet_id"] = volc.StringValue(detail.SubnetId)
	if output.ChargeDetail != nil {
		attributes["billing_mode"] = volc.StringValue(output.ChargeDetail.ChargeType)
		setPosture(row, "automatic_renewal_enabled", volc.BoolValue(output.ChargeDetail.AutoRenew))
	}
	nodeIDs := make([]string, 0, len(output.Nodes))
	zones := []string{volc.StringValue(detail.ZoneId)}
	for _, node := range output.Nodes {
		if node != nil {
			nodeIDs = append(nodeIDs, volc.StringValue(node.NodeId))
			zones = append(zones, volc.StringValue(node.ZoneId))
		}
	}
	replicaIDs := make([]string, 0, len(output.DisasterRecoveryInstances))
	for _, replica := range output.DisasterRecoveryInstances {
		if replica != nil {
			replicaIDs = append(replicaIDs, volc.StringValue(replica.InstanceId))
		}
	}
	setRelated(row, "node_ids", nodeIDs)
	setRelated(row, "replica_ids", replicaIDs)
	setRelated(row, "availability_zones", zones)
	setPosture(row, "multi_zone", len(uniqueText(zones, 100)) > 1)
	setPosture(row, "deletion_protection", strings.EqualFold(volc.StringValue(detail.DeletionProtection), "Enabled"))
	setPosture(row, "automatic_minor_version_upgrade", strings.EqualFold(volc.StringValue(detail.AutoUpgradeMinorVersion), "Enabled"))
	row["native"].(map[string]any)["instance_type"] = volc.StringValue(detail.NodeSpec)
	return row
}

func (a *volcengineAdapter) volcengineVKEDetailRow(detail *vke.ItemForListClustersOutput, region, account string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(detail.Id), volc.StringValue(detail.Name), "vke", "VKE::Cluster", "kubernetes", "cluster", region, account)
	if detail.Status != nil {
		row["state"] = strings.ToLower(volc.StringValue(detail.Status.Phase))
	}
	setDetailTime(row, "created_at", volc.StringValue(detail.CreateTime), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", volc.StringValue(detail.UpdateTime), time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, tag := range detail.Tags {
		if tag != nil && tag.Key != nil {
			tags[volc.StringValue(tag.Key)] = volc.StringValue(tag.Value)
		}
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = volc.StringValue(detail.KubernetesVersion)
	attributes["cluster_type"] = volc.StringValue(detail.Type)
	if detail.ClusterConfig != nil {
		attributes["vpc_id"] = volc.StringValue(detail.ClusterConfig.VpcId)
		setRelated(row, "subnet_ids", volcStringValues(detail.ClusterConfig.SubnetIds))
		setRelated(row, "security_group_ids", volcStringValues(detail.ClusterConfig.SecurityGroupIds))
		setPosture(row, "public_endpoint_enabled", volc.BoolValue(detail.ClusterConfig.ApiServerPublicAccessEnabled))
		setPosture(row, "private_endpoint_enabled", detail.ClusterConfig.ApiServerEndpoints != nil && detail.ClusterConfig.ApiServerEndpoints.PrivateIp != nil)
		setPosture(row, "resource_public_access_default_enabled", volc.BoolValue(detail.ClusterConfig.ResourcePublicAccessDefaultEnabled))
	}
	if detail.PodsConfig != nil {
		attributes["network_plugin"] = volc.StringValue(detail.PodsConfig.PodNetworkMode)
		if detail.PodsConfig.FlannelConfig != nil {
			podCIDRs := volcStringValues(detail.PodsConfig.FlannelConfig.PodCidrs)
			if len(podCIDRs) > 0 {
				attributes["pod_cidr"] = podCIDRs[0]
			}
			setRelated(row, "pod_cidrs", podCIDRs)
		}
	}
	if detail.ServicesConfig != nil {
		serviceCIDRs := volcStringValues(detail.ServicesConfig.ServiceCidrsv4)
		if len(serviceCIDRs) > 0 {
			attributes["service_cidr"] = serviceCIDRs[0]
		}
		setRelated(row, "service_cidrs", serviceCIDRs)
	}
	logging := false
	audit := false
	if detail.LoggingConfig != nil {
		for _, setup := range detail.LoggingConfig.LogSetups {
			if setup != nil && volc.BoolValue(setup.Enabled) {
				logging = true
				if strings.Contains(strings.ToLower(volc.StringValue(setup.LogType)), "audit") {
					audit = true
				}
			}
		}
	}
	setPosture(row, "control_plane_logging_enabled", logging)
	setPosture(row, "audit_enabled", audit)
	setPosture(row, "deletion_protection", volc.BoolValue(detail.DeleteProtectionEnabled))
	return row
}

func volcStringValues(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, volc.StringValue(value))
	}
	return result
}
