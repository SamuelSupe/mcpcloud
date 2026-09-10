package providers

import "strings"

// Inventory types are exact Resource Center identifiers, not substring hints.
func classifyAlibabaInventory(nativeType string) (string, string) {
	switch nativeType {
	case "ACS::ECS::Instance":
		return "compute", "instance"
	case "ACS::ECS::Disk":
		return "compute", "disk"
	case "ACS::OSS::Bucket":
		return "storage", "bucket"
	case "ACS::ACK::Cluster":
		return "kubernetes", "cluster"
	case "ACS::ACK::NodePool":
		return "kubernetes", "node_pool"
	case "ACS::RDS::DBInstance", "ACS::PolarDB::DBCluster":
		return "database", "database"
	case "ACS::Redis::DBInstance":
		return "database", "cache"
	case "ACS::SLB::LoadBalancer", "ACS::ALB::LoadBalancer", "ACS::NLB::LoadBalancer":
		return "network", "load_balancer"
	case "ACS::EIP::EipAddress":
		return "network", "public_ip"
	case "ACS::VPC::VSwitch":
		return "network", "subnet"
	case "ACS::ECS::SecurityGroup":
		return "network", "security_group"
	case "ACS::RAM::User", "ACS::RAM::Group":
		return "iam", "user"
	case "ACS::RAM::Role":
		return "iam", "role"
	case "ACS::RAM::Policy":
		return "iam", "policy"
	case "ACS::CMS::Alarm":
		return "monitoring", "alarm"
	case "ACS::SLS::Project":
		return "logging", "log_project"
	case "ACS::SLS::LogStore":
		return "logging", "log_group"
	}
	parts := strings.Split(nativeType, "::")
	if len(parts) == 3 && parts[0] == "ACS" {
		switch parts[1] {
		case "SLB", "ALB", "NLB", "VPC", "EIP", "NAT", "CBWP", "Ga":
			return "network", "network"
		}
	}
	return "other", "resource"
}
