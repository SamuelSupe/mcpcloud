package providers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/aliyun/alibaba-cloud-sdk-go/services/cs"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/rds"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

type alibabaRDSDetailAPI interface {
	DescribeDBInstanceAttribute(*rds.DescribeDBInstanceAttributeRequest) (*rds.DescribeDBInstanceAttributeResponse, error)
}

type alibabaCSDetailAPI interface {
	DescribeClusterDetail(*cs.DescribeClusterDetailRequest) (*cs.DescribeClusterDetailResponse, error)
}

var newAlibabaRDSDetailClient = func(profile config.Profile, region string) (alibabaRDSDetailAPI, error) {
	if profile.Credential.Source == "env" {
		access := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
		secret := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
		token := envValue(profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
		if access == "" || secret == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: "alibaba.rds.describe_db_instance_attribute", Message: "Alibaba Cloud credential environment variables are not set"}
		}
		if token != "" {
			return rds.NewClientWithStsToken(region, access, secret, token)
		}
		return rds.NewClientWithAccessKey(region, access, secret)
	}
	return rds.NewClient()
}

var newAlibabaCSDetailClient = func(profile config.Profile, region string) (alibabaCSDetailAPI, error) {
	if profile.Credential.Source == "env" {
		access := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
		secret := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
		token := envValue(profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
		if access == "" || secret == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: "alibaba.cs.describe_cluster_detail", Message: "Alibaba Cloud credential environment variables are not set"}
		}
		if token != "" {
			return cs.NewClientWithStsToken(region, access, secret, token)
		}
		return cs.NewClientWithAccessKey(region, access, secret)
	}
	return cs.NewClient()
}

type alibabaACKDetailResponse struct {
	ClusterID          string         `json:"cluster_id"`
	Name               string         `json:"name"`
	State              string         `json:"state"`
	RegionID           string         `json:"region_id"`
	ZoneID             string         `json:"zone_id"`
	ClusterType        string         `json:"cluster_type"`
	KubernetesVersion  string         `json:"current_version"`
	Created            string         `json:"created"`
	Updated            string         `json:"updated"`
	VPCID              string         `json:"vpc_id"`
	VSwitchID          string         `json:"vswitch_id"`
	VSwitchIDs         []string       `json:"vswitch_ids"`
	ContainerCIDR      string         `json:"container_cidr"`
	ServiceCIDR        string         `json:"service_cidr"`
	SecurityGroupID    string         `json:"security_group_id"`
	DeletionProtection bool           `json:"deletion_protection"`
	PrivateZone        bool           `json:"private_zone"`
	Profile            string         `json:"profile"`
	Tags               alibabaACKTags `json:"tags"`
}

// ACK has returned tags both as an object and as an array of key/value objects
// across API versions. Accept both shapes so a harmless response variation does
// not make the entire fixed-detail call fail.
type alibabaACKTags map[string]string

func (tags *alibabaACKTags) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) == 0 {
		*tags = nil
		return nil
	}
	var object map[string]string
	if err := json.Unmarshal(data, &object); err == nil {
		*tags = object
		return nil
	}
	var list []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	object = make(map[string]string, len(list))
	for _, tag := range list {
		if tag.Key != "" {
			object[tag.Key] = tag.Value
		}
	}
	*tags = object
	return nil
}

func (a *alibabaAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if err := validateDeepDetailRequest(spec, request); err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	region, err := exactDetailRegion(request.Region, a.profile.Regions, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	if spec.operation == alibabaACKMonitoringOperation {
		return a.alibabaACKMonitoring(ctx, spec, request, account, region)
	}
	if spec.operation == alibabaPolarDBDetailOperation {
		return a.alibabaPolarDBDetail(ctx, spec, request, account, region)
	}
	if spec.kind == "cache" {
		return a.alibabaRedisDetail(ctx, spec, request, account, region)
	}
	if spec.domain == "database" {
		id, err := nativeIdentifier(request.Params, "db_instance_id")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		client, err := newAlibabaRDSDetailClient(a.profile, region)
		if err != nil {
			return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
		}
		sdkRequest := rds.CreateDescribeDBInstanceAttributeRequest()
		sdkRequest.DBInstanceId = id
		setAlibabaTimeout(ctx, sdkRequest.RpcRequest)
		response, err := client.DescribeDBInstanceAttribute(sdkRequest)
		if err != nil {
			return provider.Page{}, alibabaDeepError(spec.operation, err)
		}
		if err := ctx.Err(); err != nil {
			return provider.Page{Requests: 1}, err
		}
		if response == nil {
			return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
		}
		for _, instance := range response.Items.DBInstanceAttribute {
			if instance.DBInstanceId == id && instance.RegionId == region {
				return oneDeepDetailPage(a.alibabaRDSDetailRow(instance, account)), nil
			}
		}
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	id, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	client, err := newAlibabaCSDetailClient(a.profile, region)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	sdkRequest := cs.CreateDescribeClusterDetailRequest()
	sdkRequest.ClusterId = id
	setAlibabaROATimeout(ctx, sdkRequest.SetReadTimeout, sdkRequest.SetConnectTimeout)
	response, err := client.DescribeClusterDetail(sdkRequest)
	if err != nil {
		return provider.Page{}, alibabaDeepError(spec.operation, err)
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{Requests: 1}, err
	}
	if response == nil || response.BaseResponse == nil {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	var detail alibabaACKDetailResponse
	if err := json.Unmarshal(response.GetHttpContentBytes(), &detail); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: "ACK cluster detail response is invalid"}
	}
	if detail.ClusterID != id || detail.RegionID != region {
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	return oneDeepDetailPage(a.alibabaACKDetailRow(detail, account, region)), nil
}

func setAlibabaROATimeout(ctx context.Context, read, connect func(time.Duration)) {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 {
			read(remaining)
			connect(minDuration(10*time.Second, remaining))
		}
	}
}

func alibabaDeepError(operation string, err error) error {
	return &provider.Error{Code: "alibaba_api_error", Operation: operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
}

func (a *alibabaAdapter) alibabaRDSDetailRow(instance rds.DBInstanceAttribute, account string) map[string]any {
	name := instance.DBInstanceDescription
	if name == "" {
		name = instance.DBInstanceId
	}
	row := newDeepDetailRow(a.Provider(), a.name, instance.DBInstanceId, name, "rds", "ALIYUN::RDS::DBInstance", "database", "database", instance.RegionId, account)
	row["zone"] = instance.ZoneId
	row["state"] = strings.ToLower(instance.DBInstanceStatus)
	setDetailTime(row, "created_at", instance.CreationTime, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = strings.ToLower(instance.Engine)
	attributes["engine_version"] = instance.EngineVersion
	attributes["instance_type"] = instance.DBInstanceClass
	attributes["cpu_count"] = instance.DBInstanceCPU
	attributes["memory_mb"] = instance.DBInstanceMemory
	attributes["storage_gb"] = instance.DBInstanceStorage
	attributes["vpc_id"] = instance.VpcId
	attributes["subnet_id"] = instance.VSwitchId
	attributes["billing_mode"] = instance.PayType
	setPosture(row, "multi_zone", len(instance.SlaveZones.SlaveZone) > 0)
	setPosture(row, "publicly_accessible", strings.EqualFold(instance.DBInstanceNetType, "Internet"))
	setPosture(row, "deletion_protection", instance.DeletionProtection)
	setPosture(row, "automatic_minor_version_upgrade", strings.EqualFold(instance.AutoUpgradeMinorVersion, "Auto"))
	setPosture(row, "transport_encryption_required", strings.EqualFold(instance.ConnectionMode, "Safe"))
	replicas := make([]string, 0, len(instance.ReadOnlyDBInstanceIds.ReadOnlyDBInstanceId))
	for _, replica := range instance.ReadOnlyDBInstanceIds.ReadOnlyDBInstanceId {
		replicas = append(replicas, replica.DBInstanceId)
	}
	zones := []string{instance.MasterZone}
	for _, zone := range instance.SlaveZones.SlaveZone {
		zones = append(zones, zone.ZoneId)
	}
	setRelated(row, "replica_ids", replicas)
	setRelated(row, "availability_zones", zones)
	row["native"].(map[string]any)["instance_type"] = instance.DBInstanceClass
	return row
}

func (a *alibabaAdapter) alibabaACKDetailRow(detail alibabaACKDetailResponse, account, fallbackRegion string) map[string]any {
	region := detail.RegionID
	if region == "" {
		region = fallbackRegion
	}
	row := newDeepDetailRow(a.Provider(), a.name, detail.ClusterID, detail.Name, "cs", "ALIYUN::CS::Cluster", "kubernetes", "cluster", region, account)
	row["zone"] = detail.ZoneID
	row["state"] = strings.ToLower(detail.State)
	row["tags"] = stringTags(map[string]string(detail.Tags))
	setDetailTime(row, "created_at", detail.Created, time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", detail.Updated, time.RFC3339, time.RFC3339Nano)
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = detail.KubernetesVersion
	attributes["cluster_type"] = detail.ClusterType
	attributes["network_plugin"] = detail.Profile
	attributes["vpc_id"] = detail.VPCID
	attributes["pod_cidr"] = detail.ContainerCIDR
	attributes["service_cidr"] = detail.ServiceCIDR
	setRelated(row, "subnet_ids", append(detail.VSwitchIDs, detail.VSwitchID))
	setRelated(row, "security_group_ids", []string{detail.SecurityGroupID})
	setPosture(row, "deletion_protection", detail.DeletionProtection)
	setPosture(row, "private_endpoint_enabled", detail.PrivateZone)
	return row
}
