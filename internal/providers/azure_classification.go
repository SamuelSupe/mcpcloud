package providers

import "strings"

// Provider namespaces also own child/configuration resources. Use ARM types,
// never a substring such as "compute", "ces" or "log", to identify products.
func classifyAzure(resourceType string) (string, string) {
	switch strings.ToLower(resourceType) {
	case "microsoft.compute/virtualmachines":
		return "compute", "instance"
	case "microsoft.compute/disks":
		return "compute", "disk"
	case "microsoft.storage/storageaccounts":
		return "storage", "bucket"
	case "microsoft.storage/storageaccounts/blobservices/containers":
		return "storage", "container"
	case "microsoft.containerservice/managedclusters":
		return "kubernetes", "cluster"
	case "microsoft.containerservice/managedclusters/agentpools":
		return "kubernetes", "node_pool"
	case "microsoft.dbforpostgresql/flexibleservers", "microsoft.dbforpostgresql/servers", "microsoft.dbformysql/flexibleservers", "microsoft.dbformysql/servers", "microsoft.sql/servers", "microsoft.sql/servers/databases", "microsoft.documentdb/databaseaccounts":
		return "database", "database"
	case "microsoft.cache/redis", "microsoft.cache/redisenterprise":
		return "database", "cache"
	case "microsoft.network/virtualnetworks/subnets":
		return "network", "subnet"
	case "microsoft.network/networksecuritygroups", "microsoft.network/azurefirewalls":
		return "network", "security_group"
	case "microsoft.network/publicipaddresses":
		return "network", "public_ip"
	case "microsoft.network/loadbalancers", "microsoft.network/applicationgateways", "microsoft.network/frontdoors":
		return "network", "load_balancer"
	case "microsoft.network/virtualnetworks", "microsoft.network/natgateways", "microsoft.network/networkinterfaces", "microsoft.network/routetables", "microsoft.network/publicipprefixes", "microsoft.network/privateendpoints", "microsoft.network/privatelinkservices":
		return "network", "network"
	case "microsoft.authorization/roleassignments":
		return "iam", "binding"
	case "microsoft.authorization/roledefinitions":
		return "iam", "role"
	case "microsoft.managedidentity/userassignedidentities":
		return "iam", "service_account"
	case "microsoft.insights/metricalerts", "microsoft.insights/activitylogalerts", "microsoft.insights/scheduledqueryrules", "microsoft.alertsmanagement/smartdetectoralertrules":
		return "monitoring", "alarm"
	case "microsoft.operationalinsights/workspaces", "microsoft.insights/components":
		return "logging", "log_project"
	case "microsoft.insights/diagnosticsettings", "microsoft.insights/datacollectionrules", "microsoft.insights/datacollectionendpoints":
		return "logging", "log_sink"
	default:
		return "other", "resource"
	}
}
