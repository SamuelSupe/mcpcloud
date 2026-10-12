package providers

import "strings"

// Use exact asset types: a compute service also owns networking resources,
// and names such as BackendBucket or InstanceGroup must not imply VM/Bucket.
func classifyGCP(assetType string) (string, string) {
	switch strings.ToLower(assetType) {
	case "compute.googleapis.com/instance":
		return "compute", "instance"
	case "compute.googleapis.com/disk":
		return "compute", "disk"
	case "storage.googleapis.com/bucket":
		return "storage", "bucket"
	case "container.googleapis.com/cluster":
		return "kubernetes", "cluster"
	case "container.googleapis.com/nodepool":
		return "kubernetes", "node_pool"
	case "sqladmin.googleapis.com/instance", "sqladmin.googleapis.com/databaseinstance":
		return "database", "database"
	case "redis.googleapis.com/instance", "redis.googleapis.com/cluster":
		return "database", "cache"
	case "compute.googleapis.com/subnetwork":
		return "network", "subnet"
	case "compute.googleapis.com/firewall", "compute.googleapis.com/firewallpolicy":
		return "network", "security_group"
	case "compute.googleapis.com/address", "compute.googleapis.com/globaladdress":
		return "network", "public_ip"
	case "compute.googleapis.com/forwardingrule", "compute.googleapis.com/globalforwardingrule", "compute.googleapis.com/backendservice", "compute.googleapis.com/backendbucket", "compute.googleapis.com/targetpool", "compute.googleapis.com/urlmap", "compute.googleapis.com/targethttpproxy", "compute.googleapis.com/targethttpsproxy", "compute.googleapis.com/targettcpproxy", "compute.googleapis.com/targetsslproxy":
		return "network", "load_balancer"
	case "compute.googleapis.com/network", "compute.googleapis.com/router", "compute.googleapis.com/route", "compute.googleapis.com/healthcheck", "compute.googleapis.com/httphealthcheck", "compute.googleapis.com/httpshealthcheck", "compute.googleapis.com/networkendpointgroup":
		return "network", "network"
	case "monitoring.googleapis.com/alertpolicy":
		return "monitoring", "alarm"
	case "logging.googleapis.com/logbucket":
		return "logging", "log_group"
	case "logging.googleapis.com/logsink":
		return "logging", "log_sink"
	case "iam.googleapis.com/serviceaccount":
		return "iam", "service_account"
	case "iam.googleapis.com/role":
		return "iam", "role"
	default:
		return "other", "resource"
	}
}
