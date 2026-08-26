package providers

import (
	"context"
	"strings"
	"time"

	aliecs "github.com/aliyun/alibaba-cloud-sdk-go/services/ecs"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

type alibabaECSDetailAPI interface {
	DescribeInstanceAttribute(*aliecs.DescribeInstanceAttributeRequest) (*aliecs.DescribeInstanceAttributeResponse, error)
}

var newAlibabaECSDetailClient = func(profile config.Profile, region string) (alibabaECSDetailAPI, error) {
	if profile.Credential.Source == "env" {
		access := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID")
		secret := envValue(profile, "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET")
		token := envValue(profile, "ALIBABA_CLOUD_SECURITY_TOKEN", "ALIBABA_CLOUD_SECURITY_TOKEN")
		if access == "" || secret == "" {
			return nil, &provider.Error{Code: "missing_credentials", Operation: "alibaba.ecs.describe_instance_attribute", Message: "Alibaba Cloud credential environment variables are not set"}
		}
		if token != "" {
			return aliecs.NewClientWithStsToken(region, access, secret, token)
		}
		return aliecs.NewClientWithAccessKey(region, access, secret)
	}
	return aliecs.NewClient()
}

func (a *alibabaAdapter) describeInstanceAttribute(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
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
	client, err := newAlibabaECSDetailClient(a.profile, region)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	sdkRequest := aliecs.CreateDescribeInstanceAttributeRequest()
	sdkRequest.InstanceId = instanceID
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return provider.Page{}, ctx.Err()
		}
		sdkRequest.SetReadTimeout(remaining)
		sdkRequest.SetConnectTimeout(minDuration(10*time.Second, remaining))
	}
	response, err := client.DescribeInstanceAttribute(sdkRequest)
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "alibaba_api_error", Operation: spec.operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	if err := ctx.Err(); err != nil {
		return provider.Page{}, err
	}
	if response == nil || response.InstanceId == "" {
		return provider.Page{Requests: 1}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: "ECS instance was not returned"}
	}
	return provider.Page{Rows: []map[string]any{a.alibabaInstanceDetailRow(response, account)}, Scanned: 1, Requests: 1}, nil
}

func (a *alibabaAdapter) alibabaInstanceDetailRow(detail *aliecs.DescribeInstanceAttributeResponse, account string) map[string]any {
	row := newInstanceDetailRow(a.Provider(), a.name, detail.InstanceId, detail.InstanceName, "ecs", "ALIYUN::ECS::Instance", detail.RegionId, account)
	row["zone"] = detail.ZoneId
	row["state"] = strings.ToLower(detail.Status)
	setDetailTime(row, "created_at", detail.CreationTime, time.RFC3339, time.RFC3339Nano)
	privateIPs := append([]string(nil), detail.VpcAttributes.PrivateIpAddress.IpAddress...)
	for _, address := range detail.InnerIpAddress.IpAddress {
		privateIPs = appendUniqueText(privateIPs, address)
	}
	publicIPs := append([]string(nil), detail.PublicIpAddress.IpAddress...)
	publicIPs = appendUniqueText(publicIPs, detail.EipAddress.IpAddress)
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = detail.InstanceType
	attributes["cpu_count"] = detail.Cpu
	attributes["memory_mb"] = detail.Memory
	attributes["image_id"] = detail.ImageId
	attributes["vpc_id"] = detail.VpcAttributes.VpcId
	attributes["subnet_id"] = detail.VpcAttributes.VSwitchId
	attributes["private_ip_addresses"] = privateIPs
	attributes["public_ip_addresses"] = publicIPs
	attributes["security_group_ids"] = append([]string(nil), detail.SecurityGroupIds.SecurityGroupId...)
	attributes["billing_mode"] = detail.InstanceChargeType
	setRelated(row, "security_group_ids", append([]string(nil), detail.SecurityGroupIds.SecurityGroupId...))
	row["native"].(map[string]any)["instance_type"] = detail.InstanceType
	return row
}
