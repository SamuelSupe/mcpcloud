package providers

import (
	"errors"
	"fmt"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type nativeProductSpec struct {
	suffix      string
	domain      string
	kinds       []string
	source      model.Source
	description string
}

var nativeProductTypeOverrides = map[string]map[string]struct{}{
	"alibaba.compute.list_instances": {"ACS::ECS::Instance": {}},
	"alibaba.compute.list_disks":     {"ACS::ECS::Disk": {}},
	"alibaba.storage.list_buckets":   {"ACS::OSS::Bucket": {}},
	"alibaba.network.list_resources": {
		"ACS::ALB::LoadBalancer":  {},
		"ACS::ECS::SecurityGroup": {},
		"ACS::EIP::EipAddress":    {},
		"ACS::NAT::NatGateway":    {},
		"ACS::NLB::LoadBalancer":  {},
		"ACS::SLB::LoadBalancer":  {},
		"ACS::VPC::VPC":           {},
		"ACS::VPC::VSwitch":       {},
	},
	"alibaba.iam.list_resources": {
		"ACS::RAM::Group":  {},
		"ACS::RAM::Policy": {},
		"ACS::RAM::Role":   {},
		"ACS::RAM::User":   {},
	},
	"alibaba.database.list_resources": {
		"ACS::PolarDB::DBCluster": {},
		"ACS::RDS::DBInstance":    {},
		"ACS::Redis::DBInstance":  {},
	},
	"alibaba.kubernetes.list_resources": {"ACS::ACK::Cluster": {}},
	"alibaba.monitoring.list_alarms":    {"ACS::CMS::Alarm": {}},
	"alibaba.logging.list_resources": {
		"ACS::SLS::LogStore": {},
		"ACS::SLS::Project":  {},
	},
	"volcengine.compute.list_instances": {"Volcengine::ECS::Instance": {}},
	"volcengine.compute.list_disks":     {"Volcengine::StorageEBS::Volume": {}},
	"volcengine.storage.list_buckets":   {"Volcengine::TOS::Bucket": {}},
	"volcengine.iam.list_resources": {
		"Volcengine::IAM::Group":  {},
		"Volcengine::IAM::Policy": {},
		"Volcengine::IAM::Role":   {},
		"Volcengine::IAM::User":   {},
	},
}

var nativeProductCatalog = []nativeProductSpec{
	{suffix: "compute.list_instances", domain: "compute", kinds: []string{"instance"}, source: model.SourceResources, description: "compute instances"},
	{suffix: "compute.list_disks", domain: "compute", kinds: []string{"disk"}, source: model.SourceResources, description: "block storage disks"},
	{suffix: "storage.list_buckets", domain: "storage", kinds: []string{"bucket"}, source: model.SourceResources, description: "object storage buckets"},
	{suffix: "network.list_resources", domain: "network", kinds: []string{"network", "subnet", "security_group", "public_ip", "load_balancer"}, source: model.SourceResources, description: "VPCs, subnets, security groups, public IPs, and load balancers"},
	{suffix: "iam.list_resources", domain: "iam", kinds: []string{"user", "role", "service_account", "policy", "binding"}, source: model.SourceIAM, description: "cloud-native IAM users, roles, service accounts, policies, and bindings"},
	{suffix: "database.list_resources", domain: "database", kinds: []string{"database", "cache"}, source: model.SourceResources, description: "managed relational databases and Redis-like caches"},
	{suffix: "kubernetes.list_resources", domain: "kubernetes", kinds: []string{"cluster", "node_pool"}, source: model.SourceResources, description: "managed Kubernetes clusters and node pools"},
	{suffix: "monitoring.list_alarms", domain: "monitoring", kinds: []string{"alarm"}, source: model.SourceResources, description: "monitoring and alert-rule configuration"},
	{suffix: "logging.list_resources", domain: "logging", kinds: []string{"log_project", "log_group", "log_index", "log_sink"}, source: model.SourceResources, description: "logging projects, groups, indexes, and delivery configuration"},
}

func nativeProductOperations(cloud model.Provider, backend string) []provider.Operation {
	operations := make([]provider.Operation, 0, len(nativeProductCatalog))
	for _, spec := range nativeProductCatalog {
		operations = append(operations, provider.Operation{
			Name:        nativeProductOperationName(cloud, spec),
			Provider:    cloud,
			Service:     backend,
			Description: fmt.Sprintf("List indexed %s through %s; returns normalized control-plane metadata", spec.description, backend),
			Parameters:  cloneNativeSchema(nativeProductParameters(cloud)),
		})
	}
	return operations
}

func nativeProductFor(cloud model.Provider, operation string) (nativeProductSpec, bool) {
	for _, spec := range nativeProductCatalog {
		if operation == nativeProductOperationName(cloud, spec) {
			return spec, true
		}
	}
	return nativeProductSpec{}, false
}

func nativeProductOperationNames(cloud model.Provider, source model.Source) []string {
	names := make([]string, 0, len(nativeProductCatalog))
	for _, spec := range nativeProductCatalog {
		if spec.source == source {
			names = append(names, nativeProductOperationName(cloud, spec))
		}
	}
	return names
}

func validateNativeProductRequest(cloud model.Provider, operation string, params map[string]any) error {
	return (provider.Operation{Name: operation, Parameters: nativeProductParameters(cloud)}).ValidateParams(params)
}

func completeNativeProduct(page provider.Page, err error, spec nativeProductSpec, operation string) (provider.Page, error) {
	if err != nil {
		return page, attributeNativeProductError(err, operation)
	}
	rows := page.Rows[:0]
	for _, row := range page.Rows {
		domain, _ := row["domain"].(string)
		kind, _ := row["kind"].(string)
		if domain == spec.domain && stringIn(spec.kinds, kind) && nativeProductTypeAllowed(row, operation) {
			rows = append(rows, row)
		}
	}
	page.Rows = rows
	return page, nil
}

func nativeProductTypeAllowed(row map[string]any, operation string) bool {
	allowed, ok := nativeProductTypeOverrides[operation]
	if !ok {
		return true
	}
	native, ok := row["native"].(map[string]any)
	if !ok {
		return false
	}
	nativeType, _ := native["resource_type"].(string)
	_, ok = allowed[nativeType]
	return ok
}

func attributeNativeProductError(err error, operation string) error {
	var providerErr *provider.Error
	if errors.As(err, &providerErr) {
		copy := *providerErr
		copy.Operation = operation
		return &copy
	}
	return fmt.Errorf("operation %q: %w", operation, err)
}

func nativeProductOperationName(cloud model.Provider, spec nativeProductSpec) string {
	return string(cloud) + "." + spec.suffix
}

func nativeProductParameters(cloud model.Provider) map[string]any {
	stringParam := func() map[string]any { return map[string]any{"type": "string"} }
	switch cloud {
	case model.ProviderGCP:
		return map[string]any{"scope": stringParam()}
	case model.ProviderAzure:
		return map[string]any{
			"subscriptions":  map[string]any{"type": "array", "items": "string"},
			"resource_group": stringParam(),
			"name":           stringParam(),
			"location":       stringParam(),
		}
	case model.ProviderAlibaba:
		return map[string]any{
			"view":              stringParam(),
			"resource_id":       stringParam(),
			"resource_name":     stringParam(),
			"resource_group_id": stringParam(),
			"region":            stringParam(),
		}
	case model.ProviderHuawei:
		return map[string]any{
			"region":                stringParam(),
			"id":                    stringParam(),
			"name":                  stringParam(),
			"enterprise_project_id": stringParam(),
		}
	case model.ProviderTencent:
		return map[string]any{
			"view_id":        stringParam(),
			"resource_id":    stringParam(),
			"resource_alias": stringParam(),
			"region":         stringParam(),
			"zone":           stringParam(),
			"vpc_id":         stringParam(),
			"subnet_id":      stringParam(),
		}
	case model.ProviderVolcengine:
		return map[string]any{
			"resource_id":  stringParam(),
			"region":       stringParam(),
			"project_name": stringParam(),
		}
	default:
		return map[string]any{}
	}
}

func cloneNativeSchema(schema map[string]any) map[string]any {
	copy := make(map[string]any, len(schema))
	for name, raw := range schema {
		fields, _ := raw.(map[string]any)
		fieldCopy := make(map[string]any, len(fields))
		for key, value := range fields {
			fieldCopy[key] = value
		}
		copy[name] = fieldCopy
	}
	return copy
}
