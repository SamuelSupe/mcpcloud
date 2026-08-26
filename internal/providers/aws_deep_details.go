package providers

import (
	"context"
	"strings"

	awsbase "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"mcpcloud/internal/provider"
)

type awsRDSDetailAPI interface {
	DescribeDBInstances(context.Context, *rds.DescribeDBInstancesInput, ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

type awsEKSDetailAPI interface {
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error)
}

var newAWSRDSDetailClient = func(cfg awsbase.Config) awsRDSDetailAPI { return rds.NewFromConfig(cfg) }
var newAWSEKSDetailClient = func(cfg awsbase.Config) awsEKSDetailAPI { return eks.NewFromConfig(cfg) }

func (a *awsAdapter) readDeepDetail(ctx context.Context, request provider.NativeRequest, spec deepDetailSpec) (provider.Page, error) {
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
	cfg, err := a.sdkConfig(ctx, region, spec.operation)
	if err != nil {
		return provider.Page{}, attributeInstanceDetailError(err, spec.operation)
	}
	switch spec.domain {
	case "database":
		identifier, err := nativeIdentifier(request.Params, "db_instance_identifier")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		output, err := newAWSRDSDetailClient(cfg).DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: &identifier})
		if err != nil {
			return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), spec.operation)
		}
		for _, instance := range output.DBInstances {
			if awsbase.ToString(instance.DBInstanceIdentifier) == identifier {
				return oneDeepDetailPage(a.awsRDSDetailRow(instance, region, account)), nil
			}
		}
	case "kubernetes":
		name, err := nativeIdentifier(request.Params, "name")
		if err != nil {
			return provider.Page{}, deepParameterError(spec.operation, err)
		}
		output, err := newAWSEKSDetailClient(cfg).DescribeCluster(ctx, &eks.DescribeClusterInput{Name: &name})
		if err != nil {
			return provider.Page{}, attributeInstanceDetailError(classifyAWSError(err), spec.operation)
		}
		if output.Cluster != nil && awsbase.ToString(output.Cluster.Name) == name {
			return oneDeepDetailPage(a.awsEKSDetailRow(*output.Cluster, region, account)), nil
		}
	}
	return provider.Page{Requests: 1}, deepNotFound(spec.operation, spec.kind)
}

func (a *awsAdapter) awsRDSDetailRow(instance rdstypes.DBInstance, region, account string) map[string]any {
	id := awsbase.ToString(instance.DBInstanceIdentifier)
	row := newDeepDetailRow(a.Provider(), a.name, id, id, "rds", "AWS::RDS::DBInstance", "database", "database", region, account)
	row["state"] = strings.ToLower(awsbase.ToString(instance.DBInstanceStatus))
	row["zone"] = awsbase.ToString(instance.AvailabilityZone)
	if instance.InstanceCreateTime != nil {
		row["created_at"] = instance.InstanceCreateTime.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	attributes := row["attributes"].(map[string]any)
	attributes["engine"] = awsbase.ToString(instance.Engine)
	attributes["engine_version"] = awsbase.ToString(instance.EngineVersion)
	attributes["instance_type"] = awsbase.ToString(instance.DBInstanceClass)
	attributes["storage_gb"] = awsbase.ToInt32(instance.AllocatedStorage)
	attributes["billing_mode"] = awsbase.ToString(instance.LicenseModel)
	if instance.DBSubnetGroup != nil {
		attributes["vpc_id"] = awsbase.ToString(instance.DBSubnetGroup.VpcId)
		subnets := make([]string, 0, len(instance.DBSubnetGroup.Subnets))
		for _, subnet := range instance.DBSubnetGroup.Subnets {
			subnets = append(subnets, awsbase.ToString(subnet.SubnetIdentifier))
		}
		setRelated(row, "subnet_ids", subnets)
	}
	securityGroups := make([]string, 0, len(instance.VpcSecurityGroups))
	for _, group := range instance.VpcSecurityGroups {
		securityGroups = append(securityGroups, awsbase.ToString(group.VpcSecurityGroupId))
	}
	setRelated(row, "security_group_ids", securityGroups)
	setPosture(row, "encryption_enabled", awsbase.ToBool(instance.StorageEncrypted))
	setPosture(row, "backup_enabled", awsbase.ToInt32(instance.BackupRetentionPeriod) > 0)
	setPosture(row, "backup_retention_days", awsbase.ToInt32(instance.BackupRetentionPeriod))
	setPosture(row, "multi_zone", awsbase.ToBool(instance.MultiAZ))
	setPosture(row, "publicly_accessible", awsbase.ToBool(instance.PubliclyAccessible))
	setPosture(row, "deletion_protection", awsbase.ToBool(instance.DeletionProtection))
	setPosture(row, "automatic_minor_version_upgrade", awsbase.ToBool(instance.AutoMinorVersionUpgrade))
	if instance.EnabledCloudwatchLogsExports != nil {
		auditEnabled := false
		for _, logType := range instance.EnabledCloudwatchLogsExports {
			if strings.Contains(strings.ToLower(logType), "audit") {
				auditEnabled = true
			}
		}
		setPosture(row, "logging_enabled", len(instance.EnabledCloudwatchLogsExports) > 0)
		setPosture(row, "audit_enabled", auditEnabled)
	}
	row["native"].(map[string]any)["instance_type"] = awsbase.ToString(instance.DBInstanceClass)
	return row
}

func (a *awsAdapter) awsEKSDetailRow(cluster ekstypes.Cluster, region, account string) map[string]any {
	id := awsbase.ToString(cluster.Arn)
	if id == "" {
		id = awsbase.ToString(cluster.Name)
	}
	row := newDeepDetailRow(a.Provider(), a.name, id, awsbase.ToString(cluster.Name), "eks", "AWS::EKS::Cluster", "kubernetes", "cluster", region, account)
	row["state"] = strings.ToLower(string(cluster.Status))
	if cluster.CreatedAt != nil {
		row["created_at"] = cluster.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
	}
	attributes := row["attributes"].(map[string]any)
	attributes["version"] = awsbase.ToString(cluster.Version)
	attributes["platform_version"] = awsbase.ToString(cluster.PlatformVersion)
	if cluster.KubernetesNetworkConfig != nil {
		attributes["network_plugin"] = "aws-vpc-cni"
		attributes["service_cidr"] = awsbase.ToString(cluster.KubernetesNetworkConfig.ServiceIpv4Cidr)
		if attributes["service_cidr"] == "" {
			attributes["service_cidr"] = awsbase.ToString(cluster.KubernetesNetworkConfig.ServiceIpv6Cidr)
		}
	}
	if cluster.ResourcesVpcConfig != nil {
		attributes["vpc_id"] = awsbase.ToString(cluster.ResourcesVpcConfig.VpcId)
		setRelated(row, "subnet_ids", cluster.ResourcesVpcConfig.SubnetIds)
		groups := append([]string(nil), cluster.ResourcesVpcConfig.SecurityGroupIds...)
		groups = append(groups, awsbase.ToString(cluster.ResourcesVpcConfig.ClusterSecurityGroupId))
		setRelated(row, "security_group_ids", groups)
		setPosture(row, "public_endpoint_enabled", cluster.ResourcesVpcConfig.EndpointPublicAccess)
		setPosture(row, "private_endpoint_enabled", cluster.ResourcesVpcConfig.EndpointPrivateAccess)
	}
	logging := false
	audit := false
	if cluster.Logging != nil {
		for _, setup := range cluster.Logging.ClusterLogging {
			if awsbase.ToBool(setup.Enabled) {
				logging = true
				for _, kind := range setup.Types {
					if string(kind) == "audit" {
						audit = true
					}
				}
			}
		}
	}
	setPosture(row, "control_plane_logging_enabled", logging)
	setPosture(row, "audit_enabled", audit)
	setPosture(row, "secrets_encryption_enabled", len(cluster.EncryptionConfig) > 0)
	setPosture(row, "deletion_protection", awsbase.ToBool(cluster.DeletionProtection))
	return row
}

func oneDeepDetailPage(row map[string]any) provider.Page {
	return provider.Page{Rows: []map[string]any{row}, Scanned: 1, Requests: 1}
}

func deepParameterError(operation string, err error) error {
	return &provider.Error{Code: "invalid_parameter", Operation: operation, Message: err.Error()}
}

func deepNotFound(operation, kind string) error {
	return &provider.Error{Code: "not_found", Operation: operation, Message: kind + " was not returned"}
}
