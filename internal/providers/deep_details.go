package providers

import (
	"fmt"
	"strings"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

type deepDetailSpec struct {
	operation   string
	service     string
	domain      string
	kind        string
	description string
	parameters  map[string]any
	required    []string
}

var deepDetailCatalog = map[model.Provider][]deepDetailSpec{
	model.ProviderAWS: {
		{operation: "aws.rds.describe_db_instance", service: "rds", domain: "database", kind: "database", description: "Get one RDS DB instance", parameters: detailParameters("db_instance_identifier"), required: []string{"db_instance_identifier"}},
		{operation: "aws.eks.describe_cluster", service: "eks", domain: "kubernetes", kind: "cluster", description: "Get one EKS cluster", parameters: detailParameters("name"), required: []string{"name"}},
	},
	model.ProviderGCP: {
		{operation: "gcp.sql.instances.get", service: "sqladmin", domain: "database", kind: "database", description: "Get one Cloud SQL instance", parameters: detailParameters("project_id", "instance"), required: []string{"project_id", "instance"}},
		{operation: "gcp.container.clusters.get", service: "container", domain: "kubernetes", kind: "cluster", description: "Get one GKE cluster", parameters: detailParameters("project_id", "location", "cluster"), required: []string{"project_id", "location", "cluster"}},
	},
	model.ProviderAzure: {
		{operation: "azure.dbforpostgresql.flexible_servers.get", service: "dbforpostgresql", domain: "database", kind: "database", description: "Get one Azure Database for PostgreSQL flexible server", parameters: detailParameters("subscription_id", "resource_group", "name"), required: []string{"subscription_id", "resource_group", "name"}},
		{operation: "azure.containerservice.managed_clusters.get", service: "containerservice", domain: "kubernetes", kind: "cluster", description: "Get one AKS managed cluster", parameters: detailParameters("subscription_id", "resource_group", "name"), required: []string{"subscription_id", "resource_group", "name"}},
	},
	model.ProviderAlibaba: {
		{operation: "alibaba.rds.describe_db_instance_attribute", service: "rds", domain: "database", kind: "database", description: "Get one Alibaba Cloud RDS instance", parameters: detailParameters("db_instance_id"), required: []string{"db_instance_id"}},
		{operation: "alibaba.cs.describe_cluster_detail", service: "cs", domain: "kubernetes", kind: "cluster", description: "Get one ACK cluster", parameters: detailParameters("cluster_id"), required: []string{"cluster_id"}},
	},
	model.ProviderHuawei: {
		{operation: "huawei.rds.list_instances", service: "rds", domain: "database", kind: "database", description: "Get one Huawei Cloud RDS instance using the ID-filtered ListInstances API", parameters: detailParameters("project_id", "instance_id"), required: []string{"project_id", "instance_id"}},
		{operation: "huawei.cce.show_cluster", service: "cce", domain: "kubernetes", kind: "cluster", description: "Get one CCE cluster", parameters: detailParameters("project_id", "cluster_id"), required: []string{"project_id", "cluster_id"}},
	},
	model.ProviderTencent: {
		{operation: "tencent.cdb.describe_db_instances", service: "cdb", domain: "database", kind: "database", description: "Get one TencentDB for MySQL instance", parameters: detailParameters("instance_id"), required: []string{"instance_id"}},
		{operation: "tencent.tke.describe_cluster", service: "tke", domain: "kubernetes", kind: "cluster", description: "Get one TKE cluster", parameters: detailParameters("cluster_id"), required: []string{"cluster_id"}},
	},
	model.ProviderVolcengine: {
		{operation: "volcengine.rdsmysql.describe_db_instance_detail", service: "rdsmysql", domain: "database", kind: "database", description: "Get one Volcengine RDS MySQL instance", parameters: detailParameters("instance_id"), required: []string{"instance_id"}},
		{operation: "volcengine.redis.describe_db_instance_detail", service: "redis", domain: "database", kind: "cache", description: "Get one Volcengine Cache for Redis instance", parameters: detailParameters("instance_id"), required: []string{"instance_id"}},
		{operation: "volcengine.vke.list_clusters", service: "vke", domain: "kubernetes", kind: "cluster", description: "Get one VKE cluster using the ID-filtered ListClusters API", parameters: detailParameters("cluster_id"), required: []string{"cluster_id"}},
	},
}

func deepDetailOperations(cloud model.Provider) []provider.Operation {
	specs := deepDetailCatalog[cloud]
	operations := make([]provider.Operation, 0, len(specs))
	for _, spec := range specs {
		operations = append(operations, provider.Operation{
			Name: spec.operation, Provider: cloud, Service: spec.service,
			Description: spec.description + "; returns allow-listed normalized posture metadata",
			Parameters:  cloneNativeSchema(spec.parameters),
		})
	}
	return operations
}

func deepDetailFor(cloud model.Provider, operation string) (deepDetailSpec, bool) {
	for _, spec := range deepDetailCatalog[cloud] {
		if spec.operation == operation {
			return spec, true
		}
	}
	return deepDetailSpec{}, false
}

func deepDetailOperationNames(cloud model.Provider) []string {
	specs := deepDetailCatalog[cloud]
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.operation)
	}
	return names
}

func validateDeepDetailRequest(spec deepDetailSpec, request provider.NativeRequest) error {
	operation := provider.Operation{Name: spec.operation, Parameters: spec.parameters}
	if err := operation.ValidateParams(request.Params); err != nil {
		return err
	}
	if request.PageToken != "" {
		return &provider.Error{Code: "cursor_not_supported", Operation: spec.operation, Message: "service detail operations do not accept a cursor"}
	}
	for _, name := range spec.required {
		value, err := nativeLiteral(request.Params, name)
		if err != nil {
			return &provider.Error{Code: "invalid_parameter", Operation: spec.operation, Message: err.Error()}
		}
		if strings.TrimSpace(value) == "" {
			return &provider.Error{Code: "missing_parameter", Operation: spec.operation, Message: fmt.Sprintf("parameter %s is required", name)}
		}
	}
	return nil
}

func newDeepDetailRow(cloud model.Provider, profile, id, name, service, nativeType, domain, kind, region, scopeID string) map[string]any {
	row := baseRow(cloud, profile, id, name, service, nativeType, region, scopeID, time.Now().UTC())
	row["domain"] = domain
	row["kind"] = kind
	return row
}

func setPosture(row map[string]any, key string, value any) {
	attributes := row["attributes"].(map[string]any)
	posture, _ := attributes["posture"].(map[string]any)
	if posture == nil {
		posture = map[string]any{}
		attributes["posture"] = posture
	}
	posture[key] = value
}

func setRelated(row map[string]any, key string, values []string) {
	values = uniqueText(values, 100)
	if len(values) == 0 {
		return
	}
	attributes := row["attributes"].(map[string]any)
	related, _ := attributes["related"].(map[string]any)
	if related == nil {
		related = map[string]any{}
		attributes["related"] = related
	}
	related[key] = values
}

func uniqueText(values []string, limit int) []string {
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		if value != "" && !stringIn(result, value) {
			result = append(result, value)
			if len(result) == limit {
				break
			}
		}
	}
	return result
}
