package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tencent "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	tchttp "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/http"
	tcprofile "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"

	"mcpcloud/internal/provider"
)

type tencentDetailSender interface {
	Send(tchttp.Request, tchttp.Response) error
}

var newTencentCVMDetailClient = func(credential tencent.CredentialIface, region string) tencentDetailSender {
	profile := tcprofile.NewClientProfile()
	profile.HttpProfile.Endpoint = "cvm.tencentcloudapi.com"
	profile.NetworkFailureMaxRetries = 0
	profile.RateLimitExceededMaxRetries = 0
	return tencent.NewCommonClient(credential, region, profile)
}

type tencentCVMDetailResponse struct {
	Response struct {
		InstanceSet []struct {
			InstanceID         string   `json:"InstanceId"`
			InstanceName       string   `json:"InstanceName"`
			InstanceType       string   `json:"InstanceType"`
			CPU                int      `json:"CPU"`
			Memory             int      `json:"Memory"`
			ImageID            string   `json:"ImageId"`
			OsName             string   `json:"OsName"`
			InstanceState      string   `json:"InstanceState"`
			InstanceChargeType string   `json:"InstanceChargeType"`
			CreatedTime        string   `json:"CreatedTime"`
			ExpiredTime        string   `json:"ExpiredTime"`
			PrivateIPs         []string `json:"PrivateIpAddresses"`
			PublicIPs          []string `json:"PublicIpAddresses"`
			SecurityGroupIDs   []string `json:"SecurityGroupIds"`
			Placement          struct {
				Zone string `json:"Zone"`
			} `json:"Placement"`
			VPC struct {
				VpcID    string `json:"VpcId"`
				SubnetID string `json:"SubnetId"`
			} `json:"VirtualPrivateCloud"`
			Tags []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"Tags"`
		} `json:"InstanceSet"`
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

func (a *tencentAdapter) describeCVMInstance(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
	if err := validateInstanceDetailRequest(spec, request); err != nil {
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
	instanceID, err := nativeIdentifier(request.Params, "instance_id")
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
	}
	credential, err := a.credential()
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	apiRequest := tchttp.NewCommonRequest("cvm", "2017-03-12", "DescribeInstances")
	apiRequest.SetContext(ctx)
	if err := apiRequest.SetActionParameters(map[string]any{"InstanceIds": []string{instanceID}, "Limit": 1}); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_request", Operation: spec.operation, Message: err.Error()}
	}
	apiResponse := tchttp.NewCommonResponse()
	if err := newTencentCVMDetailClient(credential, region).Send(apiRequest, apiResponse); err != nil {
		return provider.Page{}, &provider.Error{Code: "tencent_api_error", Operation: spec.operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "requestlimit", "throttl", "timeout")}
	}
	var envelope tencentCVMDetailResponse
	if err := json.Unmarshal(apiResponse.GetBody(), &envelope); err != nil {
		return provider.Page{}, &provider.Error{Code: "invalid_provider_response", Operation: spec.operation, Message: err.Error()}
	}
	if envelope.Response.Error != nil {
		code := strings.ToLower(envelope.Response.Error.Code)
		return provider.Page{}, &provider.Error{Code: envelope.Response.Error.Code, Operation: spec.operation, Message: envelope.Response.Error.Message, Retryable: containsAny(code, "requestlimit", "throttl", "internalerror")}
	}
	for _, detail := range envelope.Response.InstanceSet {
		if detail.InstanceID == instanceID {
			return provider.Page{Rows: []map[string]any{a.tencentCVMDetailRow(detail, region, account)}, Scanned: 1, Requests: 1}, nil
		}
	}
	return provider.Page{Requests: 1}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: fmt.Sprintf("CVM instance %s was not returned", instanceID)}
}

func (a *tencentAdapter) tencentCVMDetailRow(detail struct {
	InstanceID         string   `json:"InstanceId"`
	InstanceName       string   `json:"InstanceName"`
	InstanceType       string   `json:"InstanceType"`
	CPU                int      `json:"CPU"`
	Memory             int      `json:"Memory"`
	ImageID            string   `json:"ImageId"`
	OsName             string   `json:"OsName"`
	InstanceState      string   `json:"InstanceState"`
	InstanceChargeType string   `json:"InstanceChargeType"`
	CreatedTime        string   `json:"CreatedTime"`
	ExpiredTime        string   `json:"ExpiredTime"`
	PrivateIPs         []string `json:"PrivateIpAddresses"`
	PublicIPs          []string `json:"PublicIpAddresses"`
	SecurityGroupIDs   []string `json:"SecurityGroupIds"`
	Placement          struct {
		Zone string `json:"Zone"`
	} `json:"Placement"`
	VPC struct {
		VpcID    string `json:"VpcId"`
		SubnetID string `json:"SubnetId"`
	} `json:"VirtualPrivateCloud"`
	Tags []struct {
		Key   string `json:"Key"`
		Value string `json:"Value"`
	} `json:"Tags"`
}, region, account string) map[string]any {
	row := newInstanceDetailRow(a.Provider(), a.name, detail.InstanceID, detail.InstanceName, "cvm", "QCS::CVM::Instance", region, account)
	row["zone"] = detail.Placement.Zone
	row["state"] = strings.ToLower(detail.InstanceState)
	setDetailTime(row, "created_at", detail.CreatedTime, time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, tag := range detail.Tags {
		tags[tag.Key] = tag.Value
	}
	row["tags"] = tags
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = detail.InstanceType
	attributes["cpu_count"] = detail.CPU
	attributes["memory_mb"] = detail.Memory * 1024
	attributes["image_id"] = detail.ImageID
	attributes["vpc_id"] = detail.VPC.VpcID
	attributes["subnet_id"] = detail.VPC.SubnetID
	attributes["private_ip_addresses"] = append([]string(nil), detail.PrivateIPs...)
	attributes["public_ip_addresses"] = append([]string(nil), detail.PublicIPs...)
	attributes["security_group_ids"] = append([]string(nil), detail.SecurityGroupIDs...)
	attributes["os_type"] = detail.OsName
	attributes["billing_mode"] = detail.InstanceChargeType
	setRelated(row, "security_group_ids", append([]string(nil), detail.SecurityGroupIDs...))
	row["native"].(map[string]any)["instance_type"] = detail.InstanceType
	return row
}
