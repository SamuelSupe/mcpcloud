package providers

import "strings"

// classifyVolcengine keeps the normalized DSL categories tied to the
// Resource Center type names. Generic substring matching is unsafe here:
// for example, "AutoScaling" contains "tos" and "ECS::Invocation" contains
// "ecs", but neither resource is an ECS instance or a TOS bucket.
func classifyVolcengine(service, nativeType string) (string, string) {
	typeName := strings.ToLower(strings.TrimSpace(nativeType))
	serviceName := strings.ToLower(strings.TrimSpace(service))
	switch typeName {
	case "volcengine::ecs::instance":
		return "compute", "instance"
	case "volcengine::storageebs::volume":
		return "compute", "disk"
	case "volcengine::tos::bucket":
		return "storage", "bucket"
	}

	switch {
	case strings.HasPrefix(typeName, "volcengine::vpc::"):
		switch {
		case strings.HasSuffix(typeName, "::subnet"):
			return "network", "subnet"
		case strings.HasSuffix(typeName, "::securitygroup"):
			return "network", "security_group"
		case strings.HasSuffix(typeName, "::eip"):
			return "network", "public_ip"
		default:
			return "network", "network"
		}
	case strings.HasPrefix(typeName, "volcengine::clb::"), strings.HasPrefix(typeName, "volcengine::nlb::"):
		return "network", "load_balancer"
	case strings.HasPrefix(typeName, "volcengine::iam::"):
		switch {
		case strings.HasSuffix(typeName, "::policy"):
			return "iam", "policy"
		case strings.HasSuffix(typeName, "::role"):
			return "iam", "role"
		case strings.HasSuffix(typeName, "::serviceaccount") || strings.HasSuffix(typeName, "::service_account"):
			return "iam", "service_account"
		default:
			return "iam", "user"
		}
	case strings.HasPrefix(typeName, "volcengine::rds::"), strings.HasPrefix(typeName, "volcengine::rdsmysql::"):
		if strings.HasSuffix(typeName, "::instance") {
			return "database", "database"
		}
		return "other", "resource"
	case strings.HasPrefix(typeName, "volcengine::redis::"), strings.HasPrefix(typeName, "volcengine::dcs::"):
		if strings.HasSuffix(typeName, "::instance") {
			return "database", "cache"
		}
		return "other", "resource"
	case strings.HasPrefix(typeName, "volcengine::vke::"), strings.HasPrefix(typeName, "volcengine::kubernetes::"):
		if strings.HasSuffix(typeName, "::nodepool") || strings.HasSuffix(typeName, "::node_pool") {
			return "kubernetes", "node_pool"
		}
		if strings.HasSuffix(typeName, "::cluster") {
			return "kubernetes", "cluster"
		}
		return "other", "resource"
	case strings.HasPrefix(typeName, "volcengine::cloudmonitor::"), strings.HasPrefix(typeName, "volcengine::monitoring::"):
		return "monitoring", "alarm"
	case strings.HasPrefix(typeName, "volcengine::tls::"), strings.HasPrefix(typeName, "volcengine::logging::"):
		return "logging", "log_group"
	case strings.HasPrefix(typeName, "volcengine::"):
		// Keep unknown Volcengine inventory types visible to an unrestricted
		// resources query without assigning them to a misleading core kind.
		return "other", "resource"
	}

	// Some Resource Center rows use a short type name (for example
	// "snapshot") instead of the fully-qualified Volcengine type name.
	// Do not let the service name fall through to generic substring matching.
	switch serviceName {
	case "ecs", "filenas", "storage_ebs", "tos", "auto_scaling", "iam":
		return "other", "resource"
	default:
		return classify(service, nativeType)
	}
}
