package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	volctos "github.com/volcengine/ve-tos-golang-sdk/v2/tos"
	"github.com/volcengine/volcengine-go-sdk/service/rdsmysqlv2"
	"github.com/volcengine/volcengine-go-sdk/service/redis"
	"github.com/volcengine/volcengine-go-sdk/service/vke"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

type volcengineRDSDetailAPI interface {
	DescribeDBInstanceDetailWithContext(volc.Context, *rdsmysqlv2.DescribeDBInstanceDetailInput, ...request.Option) (*rdsmysqlv2.DescribeDBInstanceDetailOutput, error)
}

type volcengineVKEDetailAPI interface {
	ListClustersWithContext(volc.Context, *vke.ListClustersInput, ...request.Option) (*vke.ListClustersOutput, error)
	ListNodePoolsWithContext(volc.Context, *vke.ListNodePoolsInput, ...request.Option) (*vke.ListNodePoolsOutput, error)
	ListNodesWithContext(volc.Context, *vke.ListNodesInput, ...request.Option) (*vke.ListNodesOutput, error)
}

type volcengineRedisDetailAPI interface {
	DescribeDBInstanceDetailWithContext(volc.Context, *redis.DescribeDBInstanceDetailInput, ...request.Option) (*redis.DescribeDBInstanceDetailOutput, error)
}

type volcengineTOSDetailAPI interface {
	GetBucketInfo(context.Context, *volctos.GetBucketInfoInput) (*volctos.GetBucketInfoOutput, error)
	Close()
}

var newVolcengineRDSDetailClient = func(sess *session.Session) volcengineRDSDetailAPI { return rdsmysqlv2.New(sess) }
var newVolcengineRedisDetailClient = func(sess *session.Session) volcengineRedisDetailAPI { return redis.New(sess) }
var newVolcengineVKEDetailClient = func(sess *session.Session) volcengineVKEDetailAPI { return vke.New(sess) }
var newVolcengineTOSDetailClient = func(profile config.Profile, region, operation string) (volcengineTOSDetailAPI, error) {
	access := envValue(profile, "VOLCENGINE_ACCESS_KEY_ID", "VOLCENGINE_ACCESS_KEY_ID")
	secret := envValue(profile, "VOLCENGINE_SECRET_ACCESS_KEY", "VOLCENGINE_SECRET_ACCESS_KEY")
	if access == "" || secret == "" {
		return nil, &provider.Error{Code: "missing_credentials", Operation: operation, Message: "Volcengine credential environment variables are not set"}
	}
	credentials := volctos.NewStaticCredentials(access, secret)
	credentials.WithSecurityToken(envValue(profile, "VOLCENGINE_SESSION_TOKEN", "VOLCENGINE_SESSION_TOKEN"))
	client, err := volctos.NewClientV2(
		fmt.Sprintf("https://tos-%s.volces.com", region),
		volctos.WithRegion(region),
		volctos.WithCredentials(credentials),
		volctos.WithMaxRetryCount(0),
	)
	if err != nil {
		return nil, &provider.Error{Code: "authentication_error", Operation: operation, Message: err.Error()}
	}
	return client, nil
}

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
	if spec.service == "tos" {
		name, err := nativeIdentifier(nativeRequest.Params, "bucket_name")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		client, err := newVolcengineTOSDetailClient(a.profile, region, spec.operation)
		if err != nil {
			return provider.Page{}, err
		}
		defer client.Close()
		output, err := client.GetBucketInfo(ctx, &volctos.GetBucketInfoInput{Bucket: name})
		if err != nil {
			return provider.Page{}, volcengineDeepError(spec.operation, err)
		}
		if output == nil || output.Bucket.Name != name {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		return oneDeepDetailPage(a.volcengineTOSDetailRow(output, region, account)), nil
	}
	sess, err := a.session(region, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	if spec.service == "rdsmysql" {
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
	if spec.service == "redis" {
		id, err := nativeIdentifier(nativeRequest.Params, "instance_id")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		output, err := newVolcengineRedisDetailClient(sess).DescribeDBInstanceDetailWithContext(ctx, &redis.DescribeDBInstanceDetailInput{InstanceId: volc.String(id)})
		if err != nil {
			return provider.Page{}, volcengineDeepError(spec.operation, err)
		}
		if output == nil || volc.StringValue(output.InstanceId) != id {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		return oneDeepDetailPage(a.volcengineRedisDetailRow(output, region, account)), nil
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

func (a *volcengineAdapter) volcengineTOSDetailRow(output *volctos.GetBucketInfoOutput, fallbackRegion, account string) map[string]any {
	detail := output.Bucket
	region := detail.Location
	if region == "" {
		region = fallbackRegion
	}
	row := newDeepDetailRow(a.Provider(), a.name, detail.Name, detail.Name, "tos", "TOS::Bucket", "storage", "bucket", region, account)
	row["state"] = "available"
	if !detail.CreationDate.IsZero() {
		row["created_at"] = detail.CreationDate.UTC().Format(time.RFC3339Nano)
	}
	attributes := row["attributes"].(map[string]any)
	attributes["storage_class"] = string(detail.StorageClass)
	attributes["bucket_type"] = string(detail.Type)
	attributes["project_name"] = detail.ProjectName
	algorithm := detail.ServerSideEncryptionConfiguration.Rule.ApplyServerSideEncryptionByDefault.SSEAlgorithm
	if algorithm != "" {
		attributes["server_side_encryption_algorithm"] = algorithm
	}
	setPosture(row, "multi_zone", strings.EqualFold(string(detail.AzRedundancy), "multi-az"))
	setPosture(row, "versioning_enabled", volcengineFlagEnabled(detail.Versioning))
	setPosture(row, "cross_region_replication_enabled", volcengineFlagEnabled(string(detail.CrossRegionReplication)))
	setPosture(row, "transfer_acceleration_enabled", volcengineFlagEnabled(string(detail.TransferAcceleration)))
	setPosture(row, "access_monitor_enabled", volcengineFlagEnabled(string(detail.AccessMonitor)))
	setPosture(row, "server_side_encryption_enabled", algorithm != "")
	row["native"].(map[string]any)["instance_type"] = string(detail.Type)
	return row
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
	// DescribeDBInstanceDetail reports MySQL memory in GiB; normalized rows use MiB.
	attributes["memory_mb"] = int(volc.Int32Value(detail.Memory)) * 1024
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
	setPosture(row, "deletion_protection", volcengineFlagEnabled(volc.StringValue(detail.DeletionProtection)))
	setPosture(row, "automatic_minor_version_upgrade", volcengineFlagEnabled(volc.StringValue(detail.AutoUpgradeMinorVersion)))
	row["native"].(map[string]any)["instance_type"] = volc.StringValue(detail.NodeSpec)
	return row
}

func (a *volcengineAdapter) volcengineRedisDetailRow(detail *redis.DescribeDBInstanceDetailOutput, fallbackRegion, account string) map[string]any {
	region := volc.StringValue(detail.RegionId)
	if region == "" {
		region = fallbackRegion
	}
	row := newDeepDetailRow(a.Provider(), a.name, volc.StringValue(detail.InstanceId), volc.StringValue(detail.InstanceName), "redis", "Redis::DBInstance", "database", "cache", region, account)
	row["state"] = strings.ToLower(volc.StringValue(detail.Status))
	setDetailTime(row, "created_at", volc.StringValue(detail.CreateTime), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "expires_at", volc.StringValue(detail.ExpiredTime), time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05")
	tags := map[string]any{}
	for _, tag := range detail.Tags {
		if tag != nil && tag.Key != nil {
			tags[volc.StringValue(tag.Key)] = volc.StringValue(tag.Value)
		}
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = "redis"
	attributes["engine_version"] = volc.StringValue(detail.EngineVersion)
	attributes["instance_type"] = volc.StringValue(detail.InstanceClass)
	if detail.Capacity != nil {
		attributes["memory_mb"] = volc.Int64Value(detail.Capacity.Total)
		attributes["memory_used_mb"] = volc.Int64Value(detail.Capacity.Used)
	}
	attributes["shard_memory_mb"] = volc.Int64Value(detail.ShardCapacityV2)
	attributes["shard_count"] = int(volc.Int32Value(detail.ShardNumber))
	attributes["node_count"] = int(volc.Int32Value(detail.NodeNumber))
	attributes["max_connections_per_shard"] = int(volc.Int32Value(detail.MaxConnections))
	attributes["data_layout"] = volc.StringValue(detail.DataLayout)
	attributes["billing_mode"] = volc.StringValue(detail.ChargeType)
	attributes["maintenance_window"] = volc.StringValue(detail.MaintenanceTime)
	attributes["project_name"] = volc.StringValue(detail.ProjectName)
	attributes["vpc_id"] = volc.StringValue(detail.VpcId)
	attributes["subnet_id"] = volc.StringValue(detail.SubnetId)
	setRelated(row, "availability_zones", append(volcStringValues(detail.ZoneIds), volcengineRedisNodeZones(detail.ConfigureNodes)...))
	setPosture(row, "multi_zone", volcengineFlagEnabled(volc.StringValue(detail.MultiAZ)))
	setPosture(row, "deletion_protection", volcengineFlagEnabled(volc.StringValue(detail.DeletionProtection)))
	setPosture(row, "automatic_renewal_enabled", volc.BoolValue(detail.AutoRenew))
	setPosture(row, "sharded_cluster_enabled", volc.Int32Value(detail.ShardedCluster) == 1)
	setPosture(row, "password_free_access_enabled", strings.EqualFold(volc.StringValue(detail.VpcAuthMode), "open"))
	row["native"].(map[string]any)["instance_type"] = volc.StringValue(detail.InstanceClass)
	return row
}

func volcengineRedisNodeZones(nodes []*redis.ConfigureNodeForDescribeDBInstanceDetailOutput) []string {
	zones := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node != nil {
			zones = append(zones, volc.StringValue(node.AZ))
		}
	}
	return zones
}

func volcengineFlagEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "enable", "auto", "automatic", "true", "on", "open":
		return true
	default:
		return false
	}
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
