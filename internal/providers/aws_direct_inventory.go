package providers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func awsDirectOperations() []provider.Operation {
	names := []string{"ec2.describe_instances", "ec2.describe_volumes", "ec2.describe_vpcs", "ec2.describe_subnets", "ec2.describe_security_groups", "ec2.describe_addresses", "ec2.describe_nat_gateways", "rds.describe_db_instances", "eks.list_clusters", "elb.describe_load_balancers", "elbv2.describe_load_balancers", "elbv2.describe_target_groups", "s3.list_buckets", "elasticache.describe_cache_clusters", "elasticache.describe_replication_groups", "ecs.list_clusters"}
	ops := make([]provider.Operation, 0, len(names))
	for _, name := range names {
		service := strings.SplitN(name, ".", 2)[0]
		description := "Read regional product inventory directly from AWS; continue with the returned native token until empty."
		if name == "ec2.describe_addresses" {
			description = "Read all regional EIP allocations directly from AWS. Unpaginated: page_size does not truncate results and cursor must be empty."
		}
		ops = append(ops, operation("aws."+name, model.ProviderAWS, service, description, map[string]any{}))
	}
	// These APIs deliberately require an exact resource identifier.  They do not
	// accept arbitrary filters, endpoints, or provider actions.
	ops = append(ops,
		operation("aws.elbv2.describe_listeners", model.ProviderAWS, "elbv2", "List listeners for one ALB/NLB using its exact ARN; returns only safe listener metadata.", detailParameters("load_balancer_arn")),
		operation("aws.elbv2.describe_target_health", model.ProviderAWS, "elbv2", "Read backend health for one target group; target addresses and ports are intentionally omitted.", detailParameters("target_group_arn")),
		operation("aws.s3.get_bucket_configuration", model.ProviderAWS, "s3", "Read allow-listed bucket region, public-access, versioning, and lifecycle posture without listing objects or returning ACL principals.", detailParameters("bucket_name")),
		operation("aws.ecs.list_services", model.ProviderAWS, "ecs", "List service ARNs for one exact ECS cluster; no task definition or environment values are returned.", detailParameters("cluster_arn")),
		operation("aws.ecs.list_tasks", model.ProviderAWS, "ecs", "List task ARNs for one exact ECS cluster; no task overrides or environment values are returned.", detailParameters("cluster_arn")),
		operation("aws.eks.list_nodegroups", model.ProviderAWS, "eks", "List nodegroup names for one exact EKS cluster.", detailParameters("cluster_name")),
		operation("aws.eks.describe_nodegroup", model.ProviderAWS, "eks", "Read safe EKS nodegroup scaling and status metadata; no node IPs, labels, or kubeconfig.", detailParameters("cluster_name", "nodegroup_name")),
	)
	return ops
}

func (a *awsAdapter) readDirectInventory(ctx context.Context, req provider.NativeRequest, op provider.Operation) (provider.Page, error) {
	fail := func(message string) (provider.Page, error) {
		return provider.Page{}, &provider.Error{Code: "invalid_parameter", Operation: op.Name, Message: message}
	}
	if err := op.ValidateParams(req.Params); err != nil {
		return fail(err.Error())
	}
	for name := range op.Parameters {
		if _, err := awsDirectIdentifier(req, name); err != nil {
			return provider.Page{}, err
		}
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
	if op.Name == "aws.ec2.describe_addresses" || op.Name == "aws.elbv2.describe_target_health" || op.Name == "aws.s3.get_bucket_configuration" || op.Name == "aws.eks.describe_nodegroup" {
		if req.PageToken != "" {
			return fail("operation does not accept a pagination token")
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

func awsDirectIdentifier(req provider.NativeRequest, key string) (string, error) {
	value, err := nativeIdentifier(req.Params, key)
	if err != nil {
		return "", &provider.Error{Code: "invalid_parameter", Operation: req.Operation, Message: err.Error()}
	}
	if value == "" {
		return "", &provider.Error{Code: "missing_parameter", Operation: req.Operation, Message: key + " is required"}
	}
	return value, nil
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
	case "aws.elasticache.describe_cache_clusters", "aws.elasticache.describe_replication_groups", "aws.ecs.list_clusters", "aws.ecs.list_services", "aws.ecs.list_tasks":
		return a.fetchAWSRawService(ctx, cfg, req, region, account, limit)
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
	case "aws.eks.list_nodegroups":
		cluster, e := awsDirectIdentifier(req, "cluster_name")
		if e != nil {
			return provider.Page{}, e
		}
		out, e := eks.NewFromConfig(cfg).ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: &cluster, MaxResults: &limit, NextToken: token})
		err = e
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextToken)
			for _, name := range out.Nodegroups {
				r := row(name, "eks", "AWS::EKS::Nodegroup", "kubernetes", "nodegroup")
				r["attributes"] = map[string]any{"cluster_name": cluster}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.eks.describe_nodegroup":
		cluster, e := awsDirectIdentifier(req, "cluster_name")
		if e != nil {
			return provider.Page{}, e
		}
		name, e := awsDirectIdentifier(req, "nodegroup_name")
		if e != nil {
			return provider.Page{}, e
		}
		out, e := eks.NewFromConfig(cfg).DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: &cluster, NodegroupName: &name})
		err = e
		if err == nil && out.Nodegroup != nil {
			r := row(name, "eks", "AWS::EKS::Nodegroup", "kubernetes", "nodegroup")
			r["state"] = string(out.Nodegroup.Status)
			a := map[string]any{"cluster_name": cluster, "instance_types": out.Nodegroup.InstanceTypes, "capacity_type": string(out.Nodegroup.CapacityType)}
			if out.Nodegroup.ScalingConfig != nil {
				a["min_size"] = awsbase.ToInt32(out.Nodegroup.ScalingConfig.MinSize)
				a["max_size"] = awsbase.ToInt32(out.Nodegroup.ScalingConfig.MaxSize)
				a["desired_size"] = awsbase.ToInt32(out.Nodegroup.ScalingConfig.DesiredSize)
			}
			r["attributes"] = a
			page.Rows = append(page.Rows, r)
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
	case "aws.elbv2.describe_listeners":
		arn, e := awsDirectIdentifier(req, "load_balancer_arn")
		if e != nil {
			return provider.Page{}, e
		}
		var out *elasticloadbalancingv2.DescribeListenersOutput
		out, err = elasticloadbalancingv2.NewFromConfig(cfg).DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{LoadBalancerArn: &arn, PageSize: &limit, Marker: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextMarker)
			for _, v := range out.Listeners {
				id := awsbase.ToString(v.ListenerArn)
				r := row(id, "elasticloadbalancingv2", "AWS::ElasticLoadBalancingV2::Listener", "network", "listener")
				r["state"] = string(v.Protocol)
				r["attributes"] = map[string]any{"load_balancer_arn": arn, "port": awsbase.ToInt32(v.Port), "protocol": string(v.Protocol), "default_action_count": len(v.DefaultActions)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.elbv2.describe_target_groups":
		var out *elasticloadbalancingv2.DescribeTargetGroupsOutput
		out, err = elasticloadbalancingv2.NewFromConfig(cfg).DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{PageSize: &limit, Marker: token})
		if err == nil {
			page.NextToken = awsbase.ToString(out.NextMarker)
			for _, v := range out.TargetGroups {
				id := awsbase.ToString(v.TargetGroupArn)
				r := row(id, "elasticloadbalancingv2", "AWS::ElasticLoadBalancingV2::TargetGroup", "network", "target_group")
				r["name"] = awsbase.ToString(v.TargetGroupName)
				r["attributes"] = map[string]any{"protocol": string(v.Protocol), "port": awsbase.ToInt32(v.Port), "target_type": string(v.TargetType), "vpc_id": awsbase.ToString(v.VpcId), "health_check_enabled": awsbase.ToBool(v.HealthCheckEnabled), "health_check_protocol": string(v.HealthCheckProtocol)}
				page.Rows = append(page.Rows, r)
			}
		}
	case "aws.elbv2.describe_target_health":
		arn, e := awsDirectIdentifier(req, "target_group_arn")
		if e != nil {
			return provider.Page{}, e
		}
		var out *elasticloadbalancingv2.DescribeTargetHealthOutput
		out, err = elasticloadbalancingv2.NewFromConfig(cfg).DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: &arn})
		if err == nil {
			for index, v := range out.TargetHealthDescriptions {
				// IDs may be private IP addresses. Keep the stable ordinal scoped to the target group instead.
				id := fmt.Sprintf("%s#%d", arn, index)
				r := row(id, "elasticloadbalancingv2", "AWS::ElasticLoadBalancingV2::TargetHealth", "network", "target_health")
				state, reason := "", ""
				if v.TargetHealth != nil {
					state, reason = string(v.TargetHealth.State), string(v.TargetHealth.Reason)
				}
				r["state"] = state
				r["attributes"] = map[string]any{"target_group_arn": arn, "health_reason": reason}
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
	case "aws.s3.get_bucket_configuration":
		bucket, e := awsDirectIdentifier(req, "bucket_name")
		if e != nil {
			return provider.Page{}, e
		}
		s3client := s3.NewFromConfig(cfg)
		location, e := s3client.GetBucketLocation(ctx, &s3.GetBucketLocationInput{Bucket: &bucket})
		if e != nil {
			err = e
			break
		}
		versioning, e := s3client.GetBucketVersioning(ctx, &s3.GetBucketVersioningInput{Bucket: &bucket})
		if e != nil {
			err = e
			break
		}
		block, e := s3client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: &bucket})
		if e != nil && !awsS3OptionalConfigurationError(e, "NoSuchPublicAccessBlockConfiguration") {
			err = e
			break
		}
		lifecycle, e := s3client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: &bucket})
		if e != nil && !awsS3OptionalConfigurationError(e, "NoSuchLifecycleConfiguration") {
			err = e
			break
		}
		acl, e := s3client.GetBucketAcl(ctx, &s3.GetBucketAclInput{Bucket: &bucket})
		if e != nil {
			err = e
			break
		}
		publicACL := false
		for _, grant := range acl.Grants {
			if grant.Grantee != nil && (strings.Contains(awsbase.ToString(grant.Grantee.URI), "AllUsers") || strings.Contains(awsbase.ToString(grant.Grantee.URI), "AuthenticatedUsers")) {
				publicACL = true
			}
		}
		attributes := map[string]any{"public_acl": publicACL, "lifecycle_rule_count": 0}
		if location != nil {
			attributes["region"] = string(location.LocationConstraint)
		}
		if versioning != nil {
			attributes["versioning"] = string(versioning.Status)
			attributes["mfa_delete"] = string(versioning.MFADelete)
		}
		if lifecycle != nil {
			attributes["lifecycle_rule_count"] = len(lifecycle.Rules)
		}
		if block != nil && block.PublicAccessBlockConfiguration != nil {
			b := block.PublicAccessBlockConfiguration
			attributes["block_public_acls"] = awsbase.ToBool(b.BlockPublicAcls)
			attributes["ignore_public_acls"] = awsbase.ToBool(b.IgnorePublicAcls)
			attributes["block_public_policy"] = awsbase.ToBool(b.BlockPublicPolicy)
			attributes["restrict_public_buckets"] = awsbase.ToBool(b.RestrictPublicBuckets)
		}
		r := row(bucket, "s3", "AWS::S3::Bucket", "storage", "bucket")
		r["attributes"] = attributes
		page.Rows = append(page.Rows, r)
	default:
		return provider.Page{}, &provider.Error{Code: "operation_not_allowed", Operation: req.Operation, Message: "operation is not registered"}
	}
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), req.Operation)
	}
	page.Scanned = len(page.Rows)
	return page, nil
}

func awsS3OptionalConfigurationError(err error, code string) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == code
}
