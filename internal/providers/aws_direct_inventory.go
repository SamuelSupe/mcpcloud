package providers

import (
	"context"
	"fmt"
	"strings"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func awsDirectOperations() []provider.Operation {
	names := []string{"ec2.describe_instances", "ec2.describe_volumes", "ec2.describe_vpcs", "ec2.describe_subnets", "ec2.describe_security_groups", "ec2.describe_addresses", "ec2.describe_nat_gateways", "rds.describe_db_instances", "eks.list_clusters", "elb.describe_load_balancers", "elbv2.describe_load_balancers", "s3.list_buckets"}
	ops := make([]provider.Operation, 0, len(names))
	for _, name := range names {
		service := strings.SplitN(name, ".", 2)[0]
		description := "Read regional product inventory directly from AWS; continue with the returned native token until empty."
		if name == "ec2.describe_addresses" {
			description = "Read all regional EIP allocations directly from AWS. Unpaginated: page_size does not truncate results and cursor must be empty."
		}
		ops = append(ops, operation("aws."+name, model.ProviderAWS, service, description, map[string]any{}))
	}
	return ops
}

func (a *awsAdapter) readDirectInventory(ctx context.Context, req provider.NativeRequest, op provider.Operation) (provider.Page, error) {
	fail := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: op.Name, Message: message}
	}
	if err := op.ValidateParams(req.Params); err != nil {
		return fail(err.Error())
	}
	region, err := exactDetailRegion(req.Region, a.profile.Regions, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	account, err := singleDetailAccount(a.profile, op.Name)
	if err != nil {
		return provider.Page{}, err
	}
	min, max := 5, 1000
	if op.Service == "rds" {
		min, max = 20, 100
	}
	if op.Service == "eks" {
		min, max = 1, 100
	}
	if op.Service == "elb" || op.Service == "elbv2" {
		min, max = 1, 400
	}
	if op.Service == "s3" {
		min, max = 1, 10000
	}
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	if op.Name == "aws.ec2.describe_addresses" {
		if req.PageToken != "" {
			return fail("DescribeAddresses does not accept a pagination token")
		}
	} else if limit < min || limit > max {
		return fail(fmt.Sprintf("page size must be between %d and %d", min, max))
	}
	cfg, err := a.sdkConfig(ctx, region, op.Name)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, op.Name)
	}
	return a.fetchDirectInventory(ctx, cfg, req, region, account, int32(limit))
}

func (a *awsAdapter) fetchDirectInventory(ctx context.Context, cfg awsbase.Config, req provider.NativeRequest, region, account string, limit int32) (provider.Page, error) {
	page := provider.Page{Rows: []map[string]any{}, Requests: 1}
	var token *string
	if req.PageToken != "" {
		token = &req.PageToken
	}
	client := ec2.NewFromConfig(cfg)
	row := func(id, service, nativeType, domain, kind string) map[string]any {
		return newDeepDetailRow(a.Provider(), a.name, id, id, service, nativeType, domain, kind, region, account)
	}
	var err error
	switch req.Operation {
	case "aws.ec2.describe_instances":
		var out *ec2.DescribeInstancesOutput
		out, err = client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, res := range out.Reservations {
				for _, v := range res.Instances {
					page.Rows = append(page.Rows, a.awsInstanceDetailRow(v, region, account))
				}
			}
		}
	case "aws.ec2.describe_volumes":
		var out *ec2.DescribeVolumesOutput
		out, err = client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, v := range out.Volumes {
				r := row(awsbase.ToString(v.VolumeId), "ec2", "AWS::EC2::Volume", "compute", "disk")
				r["state"] = string(v.State)
				r["zone"] = awsbase.ToString(v.AvailabilityZone)
				r["attributes"] = map[string]any{"size_gb": awsbase.ToInt32(v.Size), "volume_type": string(v.VolumeType), "encrypted": awsbase.ToBool(v.Encrypted), "iops": awsbase.ToInt32(v.Iops)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.ec2.describe_vpcs":
		var out *ec2.DescribeVpcsOutput
		out, err = client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, v := range out.Vpcs {
				r := row(awsbase.ToString(v.VpcId), "ec2", "AWS::EC2::VPC", "network", "vpc")
				r["state"] = string(v.State)
				r["attributes"] = map[string]any{"cidr_block": awsbase.ToString(v.CidrBlock), "is_default": awsbase.ToBool(v.IsDefault)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.ec2.describe_subnets":
		var out *ec2.DescribeSubnetsOutput
		out, err = client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, v := range out.Subnets {
				r := row(awsbase.ToString(v.SubnetId), "ec2", "AWS::EC2::Subnet", "network", "subnet")
				r["state"] = string(v.State)
				r["zone"] = awsbase.ToString(v.AvailabilityZone)
				r["attributes"] = map[string]any{"vpc_id": awsbase.ToString(v.VpcId), "cidr_block": awsbase.ToString(v.CidrBlock), "available_ip_count": awsbase.ToInt32(v.AvailableIpAddressCount)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.ec2.describe_security_groups":
		var out *ec2.DescribeSecurityGroupsOutput
		out, err = client.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, v := range out.SecurityGroups {
				r := row(awsbase.ToString(v.GroupId), "ec2", "AWS::EC2::SecurityGroup", "network", "security_group")
				r["name"] = awsbase.ToString(v.GroupName)
				r["attributes"] = map[string]any{"vpc_id": awsbase.ToString(v.VpcId)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.ec2.describe_addresses":
		var out *ec2.DescribeAddressesOutput
		out, err = client.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
		if err == nil {
			for _, v := range out.Addresses {
				id := awsbase.ToString(v.AllocationId)
				if id == "" {
					id = awsbase.ToString(v.PublicIp)
				}
				r := row(id, "ec2", "AWS::EC2::EIP", "network", "eip")
				r["attributes"] = map[string]any{"public_ip": awsbase.ToString(v.PublicIp), "private_ip": awsbase.ToString(v.PrivateIpAddress), "association_id": awsbase.ToString(v.AssociationId), "instance_id": awsbase.ToString(v.InstanceId), "network_interface_id": awsbase.ToString(v.NetworkInterfaceId)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.ec2.describe_nat_gateways":
		var out *ec2.DescribeNatGatewaysOutput
		out, err = client.DescribeNatGateways(ctx, &ec2.DescribeNatGatewaysInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, v := range out.NatGateways {
				r := row(awsbase.ToString(v.NatGatewayId), "ec2", "AWS::EC2::NatGateway", "network", "nat_gateway")
				r["state"] = string(v.State)
				addresses := []map[string]any{}
				for _, ip := range v.NatGatewayAddresses {
					addresses = append(addresses, map[string]any{"allocation_id": awsbase.ToString(ip.AllocationId), "public_ip": awsbase.ToString(ip.PublicIp), "private_ip": awsbase.ToString(ip.PrivateIp)})
				}
				r["attributes"] = map[string]any{"vpc_id": awsbase.ToString(v.VpcId), "subnet_id": awsbase.ToString(v.SubnetId), "connectivity_type": string(v.ConnectivityType), "addresses": addresses}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.rds.describe_db_instances":
		var out *rds.DescribeDBInstancesOutput
		out, err = rds.NewFromConfig(cfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{MaxRecords: &limit, Marker: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.Marker)
			for _, v := range out.DBInstances {
				page.Rows = append(page.Rows, a.awsRDSDetailRow(v, region, account))
			}
		}
	case "aws.eks.list_clusters":
		var out *eks.ListClustersOutput
		out, err = eks.NewFromConfig(cfg).ListClusters(ctx, &eks.ListClustersInput{MaxResults: &limit, NextToken: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, name := range out.Clusters {
				page.Rows = append(page.Rows, row(name, "eks", "AWS::EKS::Cluster", "kubernetes", "cluster"))
			}
		}
	case "aws.elb.describe_load_balancers":
		var out *elasticloadbalancing.DescribeLoadBalancersOutput
		out, err = elasticloadbalancing.NewFromConfig(cfg).DescribeLoadBalancers(ctx, &elasticloadbalancing.DescribeLoadBalancersInput{PageSize: &limit, Marker: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextMarker)
			for _, v := range out.LoadBalancerDescriptions {
				r := row(awsbase.ToString(v.LoadBalancerName), "elasticloadbalancing", "AWS::ElasticLoadBalancing::LoadBalancer", "network", "load_balancer")
				r["attributes"] = map[string]any{"dns_name": awsbase.ToString(v.DNSName), "vpc_id": awsbase.ToString(v.VPCId), "scheme": awsbase.ToString(v.Scheme), "subnet_ids": v.Subnets, "security_group_ids": v.SecurityGroups}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.elbv2.describe_load_balancers":
		var out *elasticloadbalancingv2.DescribeLoadBalancersOutput
		out, err = elasticloadbalancingv2.NewFromConfig(cfg).DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{PageSize: &limit, Marker: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextMarker)
			for _, v := range out.LoadBalancers {
				r := row(awsbase.ToString(v.LoadBalancerArn), "elasticloadbalancingv2", "AWS::ElasticLoadBalancingV2::LoadBalancer", "network", "load_balancer")
				r["name"] = awsbase.ToString(v.LoadBalancerName)
				if v.State != nil {
					r["state"] = string(v.State.Code)
				}
				r["attributes"] = map[string]any{"dns_name": awsbase.ToString(v.DNSName), "vpc_id": awsbase.ToString(v.VpcId), "scheme": string(v.Scheme), "type": string(v.Type), "security_group_ids": v.SecurityGroups}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.s3.list_buckets":
		var out *s3.ListBucketsOutput
		out, err = s3.NewFromConfig(cfg).ListBuckets(ctx, &s3.ListBucketsInput{MaxBuckets: &limit, ContinuationToken: token, BucketRegion: &region})
		if err == nil {
			page.NextToken = awsbase.ToString(out.ContinuationToken)
			for _, v := range out.Buckets {
				if v.BucketRegion != nil && awsbase.ToString(v.BucketRegion) != region {
					return provider.Page{}, &provider.Error{Code: "scope_not_allowed", Operation: req.Operation, Message: "S3 returned a bucket outside the requested region"}
				}
				r := row(awsbase.ToString(v.Name), "s3", "AWS::S3::Bucket", "storage", "bucket")
				if v.CreationDate != nil {
					r["created_at"] = v.CreationDate.UTC().Format("2006-01-02T15:04:05Z")
				}
				page.Rows = append(page.Rows, r)
			}
		}
	default:
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), req.Operation)
	}
	page.Scanned = len(page.Rows)
	return page, nil
}
