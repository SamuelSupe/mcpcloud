package providers

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	tcprofile "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"mcpcloud/internal/provider"
)

var newTencentCDBDetailClient = func(credential tencent.CredentialIface, region string) tencentDetailSender {
	profile := tcprofile.NewClientProfile()
	profile.HttpProfile.Endpoint = "cdb.tencentcloudapi.com"
	profile.NetworkFailureMaxRetries = 0
	profile.RateLimitExceededMaxRetries = 0
	return tencent.NewCommonClient(credential, region, profile)
}

var newTencentTKEDetailClient = func(credential tencent.CredentialIface, region string) tencentDetailSender {
	profile := tcprofile.NewClientProfile()
	profile.HttpProfile.Endpoint = "tke.tencentcloudapi.com"
	profile.NetworkFailureMaxRetries = 0
	profile.RateLimitExceededMaxRetries = 0
	return tencent.NewCommonClient(credential, region, profile)
}

type tencentCDBDetailResponse struct {
	Response struct {
		Items []struct {
			InstanceID    string `json:"InstanceId"`
			InstanceName  string `json:"InstanceName"`
			Status        int    `json:"Status"`
			StatusName    string `json:"StatusName"`
			Region        string `json:"Region"`
			Zone          string `json:"Zone"`
			DeviceType    string `json:"DeviceType"`
			EngineVersion string `json:"EngineVersion"`
			CPU           int    `json:"Cpu"`
			Memory        int    `json:"Memory"`
			Volume        int    `json:"Volume"`
			PayType       int    `json:"PayType"`
			ProtectMode   int    `json:"ProtectMode"`
			DeployMode    int    `json:"DeployMode"`
			CreateTime    string `json:"CreateTime"`
			AutoRenew     int    `json:"AutoRenew"`
			VpcID         int64  `json:"VpcId"`
			UniqVpcID     string `json:"UniqVpcId"`
			SubnetID      int64  `json:"SubnetId"`
			UniqSubnetID  string `json:"UniqSubnetId"`
			RoGroups      []struct {
				RoGroupID string `json:"RoGroupId"`
			} `json:"RoGroups"`
			TagList []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"TagList"`
		} `json:"Items"`
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

type tencentTKEDetailResponse struct {
	Response struct {
		Clusters []struct {
			ClusterID              string `json:"ClusterId"`
			ClusterName            string `json:"ClusterName"`
			ClusterDescription     string `json:"ClusterDescription"`
			ClusterType            string `json:"ClusterType"`
			ClusterStatus          string `json:"ClusterStatus"`
			ClusterVersion         string `json:"ClusterVersion"`
			ClusterLevel           string `json:"ClusterLevel"`
			CreatedTime            string `json:"CreatedTime"`
			VpcID                  string `json:"VpcId"`
			ProjectID              int64  `json:"ProjectId"`
			DeletionProtection     bool   `json:"DeletionProtection"`
			ClusterNetworkSettings struct {
				ClusterCIDR               string `json:"ClusterCIDR"`
				ServiceCIDR               string `json:"ServiceCIDR"`
				Cni                       bool   `json:"Cni"`
				IgnoreClusterCIDRConflict bool   `json:"IgnoreClusterCIDRConflict"`
			} `json:"ClusterNetworkSettings"`
			TagSpecification []struct {
				Tags []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"Tags"`
			} `json:"TagSpecification"`
		} `json:"Clusters"`
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

func (a *tencentAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
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
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	if spec.domain == "database" {
		id, err := nativeIdentifier(request.Params, "instance_id")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		apiRequest := tchttp.NewCommonRequest("cdb", "2017-03-20", "DescribeDBInstances")
		apiRequest.SetContext(ctx)
		if err := apiRequest.SetActionParameters(map[string]any{"InstanceIds": []string{id}, "Limit": 1}); err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_request", Operation: spec.operation, Message: err.Error()}
		}
		response := tchttp.NewCommonResponse()
		if err := newTencentCDBDetailClient(credential, region).Send(apiRequest, response); err != nil {
			return provider.Page{}, tencentDeepError(spec.operation, err)
		}
		var envelope tencentCDBDetailResponse
		if err := json.Unmarshal(response.GetBody(), &envelope); err != nil {
			return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: err.Error()}
		}
		if err := tencentEnvelopeError(spec.operation, envelope.Response.Error); err != nil {
			return provider.Page{}, err
		}
		for _, detail := range envelope.Response.Items {
			if detail.InstanceID == id {
				return oneDeepDetailPage(a.tencentCDBDetailRow(detail, region, account)), nil
			}
		}
		return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
	}
	id, err := nativeIdentifier(request.Params, "cluster_id")
	if err != nil {
		return provider.Page{}, deepParameterError(spec.operation, err)
	}
	apiRequest := tchttp.NewCommonRequest("tke", "2018-05-25", "DescribeClusters")
	apiRequest.SetContext(ctx)
	if err := apiRequest.SetActionParameters(map[string]any{"ClusterIds": []string{id}, "Limit": 1}); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_request", Operation: spec.operation, Message: err.Error()}
	}
	response := tchttp.NewCommonResponse()
	if err := newTencentTKEDetailClient(credential, region).Send(apiRequest, response); err != nil {
		return provider.Page{}, tencentDeepError(spec.operation, err)
	}
	var envelope tencentTKEDetailResponse
	if err := json.Unmarshal(response.GetBody(), &envelope); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: err.Error()}
	}
	if err := tencentEnvelopeError(spec.operation, envelope.Response.Error); err != nil {
		return provider.Page{}, err
	}
	for _, detail := range envelope.Response.Clusters {
		if detail.ClusterID == id {
			return oneDeepDetailPage(a.tencentTKEDetailRow(detail, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
}

func tencentDeepError(operation string, err error) error {
	return &provider.Error{Code: "tencent_api_error", Operation: operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "requestlimit", "throttl", "timeout")}
}

func tencentEnvelopeError(operation string, value *struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}) error {
	if value == nil {
		return nil
	}
	return &provider.Error{Code: value.Code, Operation: operation, Message: value.Message, Retryable: containsAny(strings.ToLower(value.Code), "requestlimit", "throttl", "internalerror")}
}

func (a *tencentAdapter) tencentCDBDetailRow(detail struct {
	InstanceID    string `json:"InstanceId"`
	InstanceName  string `json:"InstanceName"`
	Status        int    `json:"Status"`
	StatusName    string `json:"StatusName"`
	Region        string `json:"Region"`
	Zone          string `json:"Zone"`
	DeviceType    string `json:"DeviceType"`
	EngineVersion string `json:"EngineVersion"`
	CPU           int    `json:"Cpu"`
	Memory        int    `json:"Memory"`
	Volume        int    `json:"Volume"`
	PayType       int    `json:"PayType"`
	ProtectMode   int    `json:"ProtectMode"`
	DeployMode    int    `json:"DeployMode"`
	CreateTime    string `json:"CreateTime"`
	AutoRenew     int    `json:"AutoRenew"`
	VpcID         int64  `json:"VpcId"`
	UniqVpcID     string `json:"UniqVpcId"`
	SubnetID      int64  `json:"SubnetId"`
	UniqSubnetID  string `json:"UniqSubnetId"`
	RoGroups      []struct {
		RoGroupID string `json:"RoGroupId"`
	} `json:"RoGroups"`
	TagList []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"TagList"`
}, fallbackRegion, account string) map[string]any {
	region := detail.Region
	if region == "" {
		region = fallbackRegion
	}
	row := newDeepDetailRow(a.Provider(), a.name, detail.InstanceID, detail.InstanceName, "cdb", "QCS::CDB::Instance", "database", "database", region, account)
	row["zone"] = detail.Zone
	row["state"] = strings.ToLower(detail.StatusName)
	setDetailTime(row, "created_at", detail.CreateTime, time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, tag := range detail.TagList {
		tags[tag.Key] = tag.Value
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = "mysql"
	attributes["engine_version"] = detail.EngineVersion
	attributes["instance_type"] = detail.DeviceType
	attributes["cpu_count"] = detail.CPU
	attributes["memory_mb"] = detail.Memory
	attributes["storage_gb"] = detail.Volume
	attributes["vpc_id"] = detail.UniqVpcID
	attributes["subnet_id"] = detail.UniqSubnetID
	attributes["billing_mode"] = detail.PayType
	replicas := make([]string, 0, len(detail.RoGroups))
	for _, group := range detail.RoGroups {
		replicas = append(replicas, group.RoGroupID)
	}
	setRelated(row, "replica_group_ids", replicas)
	setPosture(row, "multi_zone", detail.DeployMode > 0)
	setPosture(row, "high_availability_mode", detail.ProtectMode)
	setPosture(row, "automatic_renewal_enabled", detail.AutoRenew > 0)
	row["native"].(map[string]any)["instance_type"] = detail.DeviceType
	return row
}

func (a *tencentAdapter) tencentTKEDetailRow(detail struct {
	ClusterID              string `json:"ClusterId"`
	ClusterName            string `json:"ClusterName"`
	ClusterDescription     string `json:"ClusterDescription"`
	ClusterType            string `json:"ClusterType"`
	ClusterStatus          string `json:"ClusterStatus"`
	ClusterVersion         string `json:"ClusterVersion"`
	ClusterLevel           string `json:"ClusterLevel"`
	CreatedTime            string `json:"CreatedTime"`
	VpcID                  string `json:"VpcId"`
	ProjectID              int64  `json:"ProjectId"`
	DeletionProtection     bool   `json:"DeletionProtection"`
	ClusterNetworkSettings struct {
		ClusterCIDR               string `json:"ClusterCIDR"`
		ServiceCIDR               string `json:"ServiceCIDR"`
		Cni                       bool   `json:"Cni"`
		IgnoreClusterCIDRConflict bool   `json:"IgnoreClusterCIDRConflict"`
	} `json:"ClusterNetworkSettings"`
	TagSpecification []struct {
		Tags []struct {
			Key   string `json:"Key"`
			Value string `json:"Value"`
		} `json:"Tags"`
	} `json:"TagSpecification"`
}, region, account string) map[string]any {
	row := newDeepDetailRow(a.Provider(), a.name, detail.ClusterID, detail.ClusterName, "tke", "QCS::TKE::Cluster", "kubernetes", "cluster", region, account)
	row["state"] = strings.ToLower(detail.ClusterStatus)
	setDetailTime(row, "created_at", detail.CreatedTime, time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, specification := range detail.TagSpecification {
		for _, tag := range specification.Tags {
			tags[tag.Key] = tag.Value
		}
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = detail.ClusterVersion
	attributes["cluster_type"] = detail.ClusterType
	attributes["instance_type"] = detail.ClusterLevel
	attributes["vpc_id"] = detail.VpcID
	attributes["pod_cidr"] = detail.ClusterNetworkSettings.ClusterCIDR
	attributes["service_cidr"] = detail.ClusterNetworkSettings.ServiceCIDR
	if detail.ClusterNetworkSettings.Cni {
		attributes["network_plugin"] = "cni"
	}
	setPosture(row, "deletion_protection", detail.DeletionProtection)
	setPosture(row, "cidr_conflict_check_enabled", !detail.ClusterNetworkSettings.IgnoreClusterCIDRConflict)
	return row
}
