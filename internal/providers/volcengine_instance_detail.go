package providers

import (
	"context"
	"strings"
	"time"

	volcecs "github.com/volcengine/volcengine-go-sdk/service/ecs"
	volc "github.com/volcengine/volcengine-go-sdk/volcengine"
	"github.com/volcengine/volcengine-go-sdk/volcengine/request"
	"github.com/volcengine/volcengine-go-sdk/volcengine/session"

	"mcpcloud/internal/provider"
)

type volcengineECSDetailAPI interface {
	DescribeInstancesWithContext(volc.Context, *volcecs.DescribeInstancesInput, ...request.Option) (*volcecs.DescribeInstancesOutput, error)
}

var newVolcengineECSDetailClient = func(sess *session.Session) volcengineECSDetailAPI { return volcecs.New(sess) }

func (a *volcengineAdapter) describeECSInstance(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
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
	sess, err := a.session(region, spec.operation)
	if err != nil {
		return provider.Page{}, err
	}
	max := int32(1)
	output, err := newVolcengineECSDetailClient(sess).DescribeInstancesWithContext(ctx, &volcecs.DescribeInstancesInput{InstanceIds: []*string{volc.String(instanceID)}, MaxResults: &max})
	if err != nil {
		return provider.Page{}, &provider.Error{Code: "volcengine_api_error", Operation: spec.operation, Message: err.Error(), Retryable: containsAny(strings.ToLower(err.Error()), "throttl", "timeout")}
	}
	for _, detail := range output.Instances {
		if detail != nil && volc.StringValue(detail.InstanceId) == instanceID {
			return provider.Page{Rows: []map[string]any{a.volcengineECSDetailRow(detail, region, account)}, Scanned: 1, Requests: 1}, nil
		}
	}
	return provider.Page{Requests: 1}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: "ECS instance was not returned"}
}

func (a *volcengineAdapter) volcengineECSDetailRow(detail *volcecs.InstanceForDescribeInstancesOutput, region, account string) map[string]any {
	row := newInstanceDetailRow(a.Provider(), a.name, volc.StringValue(detail.InstanceId), volc.StringValue(detail.InstanceName), "ecs", "ECS::Instance", region, account)
	row["zone"] = volc.StringValue(detail.ZoneId)
	row["state"] = strings.ToLower(volc.StringValue(detail.Status))
	setDetailTime(row, "created_at", volc.StringValue(detail.CreatedAt), time.RFC3339, time.RFC3339Nano)
	setDetailTime(row, "updated_at", volc.StringValue(detail.UpdatedAt), time.RFC3339, time.RFC3339Nano)
	tags := map[string]any{}
	for _, tag := range detail.Tags {
		if tag != nil && tag.Key != nil {
			tags[volc.StringValue(tag.Key)] = volc.StringValue(tag.Value)
		}
	}
	row["tags"] = tags
	privateIPs, publicIPs, securityGroups := []string{}, []string{}, []string{}
	vpcID, subnetID := volc.StringValue(detail.VpcId), ""
	for _, networkInterface := range detail.NetworkInterfaces {
		if networkInterface == nil {
			continue
		}
		privateIPs = appendUniqueText(privateIPs, volc.StringValue(networkInterface.PrimaryIpAddress))
		for _, address := range networkInterface.Ipv6Addresses {
			privateIPs = appendUniqueText(privateIPs, volc.StringValue(address))
		}
		if vpcID == "" {
			vpcID = volc.StringValue(networkInterface.VpcId)
		}
		if subnetID == "" {
			subnetID = volc.StringValue(networkInterface.SubnetId)
		}
		for _, group := range networkInterface.SecurityGroupIds {
			securityGroups = appendUniqueText(securityGroups, volc.StringValue(group))
		}
	}
	if detail.EipAddress != nil {
		publicIPs = appendUniqueText(publicIPs, volc.StringValue(detail.EipAddress.IpAddress))
	}
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = volc.StringValue(detail.InstanceTypeId)
	attributes["cpu_count"] = int(volc.Int32Value(detail.Cpus))
	attributes["memory_mb"] = int(volc.Int32Value(detail.MemorySize))
	attributes["image_id"] = volc.StringValue(detail.ImageId)
	attributes["vpc_id"] = vpcID
	attributes["subnet_id"] = subnetID
	attributes["private_ip_addresses"] = privateIPs
	attributes["public_ip_addresses"] = publicIPs
	attributes["security_group_ids"] = securityGroups
	attributes["os_type"] = volc.StringValue(detail.OsType)
	attributes["os_name"] = volc.StringValue(detail.OsName)
	attributes["billing_mode"] = volc.StringValue(detail.InstanceChargeType)
	attributes["deletion_protection"] = volc.BoolValue(detail.DeletionProtection)
	setRelated(row, "security_group_ids", securityGroups)
	row["native"].(map[string]any)["instance_type"] = volc.StringValue(detail.InstanceTypeId)
	return row
}
