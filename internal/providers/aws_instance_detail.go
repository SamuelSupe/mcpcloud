package providers

import (
	"context"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"mcpcloud/internal/provider"
)

type awsEC2DescribeAPI interface {
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

var newAWSEC2DescribeClient = func(cfg awsbase.Config) awsEC2DescribeAPI { return ec2.NewFromConfig(cfg) }

func (a *awsAdapter) describeInstance(ctx context.Context, request provider.NativeRequest, spec instanceDetailSpec) (provider.Page, error) {
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
	cfg, err := a.sdkConfig(ctx, region, spec.operation)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	output, err := newAWSEC2DescribeClient(cfg).DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{instanceID}})
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), spec.operation)
	}
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			if awsbase.ToString(instance.InstanceId) == instanceID {
				return provider.Page{Rows: []map[string]any{a.awsInstanceDetailRow(instance, region, account)}, Scanned: 1, Requests: 1}, nil
			}
		}
	}
	return provider.Page{Scanned: 0, Requests: 1}, &provider.Error{Code: "not_found", Operation: spec.operation, Message: "EC2 instance was not returned"}
}

func (a *awsAdapter) awsInstanceDetailRow(instance types.Instance, region, account string) map[string]any {
	id := awsbase.ToString(instance.InstanceId)
	name := id
	tags := map[string]any{}
	for _, tag := range instance.Tags {
		key, value := awsbase.ToString(tag.Key), awsbase.ToString(tag.Value)
		if key != "" {
			tags[key] = value
			if key == "Name" && value != "" {
				name = value
			}
		}
	}
	row := newInstanceDetailRow(a.Provider(), a.name, id, name, "ec2", "AWS::EC2::Instance", region, account)
	row["tags"] = tags
	if instance.State != nil {
		row["state"] = string(instance.State.Name)
	}
	if instance.Placement != nil {
		row["zone"] = awsbase.ToString(instance.Placement.AvailabilityZone)
	}
	if instance.LaunchTime != nil {
		row["created_at"] = instance.LaunchTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	privateIPs, publicIPs, securityGroups, networkInterfaces, volumeIDs := []string{}, []string{}, []string{}, []string{}, []string{}
	privateIPs = appendUniqueText(privateIPs, awsbase.ToString(instance.PrivateIpAddress))
	publicIPs = appendUniqueText(publicIPs, awsbase.ToString(instance.PublicIpAddress))
	for _, group := range instance.SecurityGroups {
		securityGroups = appendUniqueText(securityGroups, awsbase.ToString(group.GroupId))
	}
	for _, networkInterface := range instance.NetworkInterfaces {
		networkInterfaces = appendUniqueText(networkInterfaces, awsbase.ToString(networkInterface.NetworkInterfaceId))
		privateIPs = appendUniqueText(privateIPs, awsbase.ToString(networkInterface.PrivateIpAddress))
		if networkInterface.Association != nil {
			publicIPs = appendUniqueText(publicIPs, awsbase.ToString(networkInterface.Association.PublicIp))
		}
	}
	for _, mapping := range instance.BlockDeviceMappings {
		if mapping.Ebs != nil {
			volumeIDs = appendUniqueText(volumeIDs, awsbase.ToString(mapping.Ebs.VolumeId))
		}
	}
	attributes := row["attributes"].(map[string]any)
	attributes["instance_type"] = string(instance.InstanceType)
	attributes["image_id"] = awsbase.ToString(instance.ImageId)
	attributes["vpc_id"] = awsbase.ToString(instance.VpcId)
	attributes["subnet_id"] = awsbase.ToString(instance.SubnetId)
	attributes["private_ip_addresses"] = privateIPs
	attributes["public_ip_addresses"] = publicIPs
	attributes["security_group_ids"] = securityGroups
	attributes["architecture"] = string(instance.Architecture)
	attributes["os_type"] = awsbase.ToString(instance.PlatformDetails)
	attributes["billing_mode"] = string(instance.InstanceLifecycle)
	setRelated(row, "network_interface_ids", networkInterfaces)
	setRelated(row, "volume_ids", volumeIDs)
	setRelated(row, "security_group_ids", securityGroups)
	if instance.CpuOptions != nil && instance.CpuOptions.CoreCount != nil && instance.CpuOptions.ThreadsPerCore != nil {
		attributes["cpu_count"] = int(*instance.CpuOptions.CoreCount * *instance.CpuOptions.ThreadsPerCore)
	}
	native := row["native"].(map[string]any)
	native["instance_type"] = string(instance.InstanceType)
	return row
}
