package providers

import "testing"

func TestAlibabaInventoryClassification(t *testing.T) {
	for _, tc := range []struct{ nativeType, domain, kind string }{
		{"ACS::ACK::Cluster", "kubernetes", "cluster"}, {"ACS::ACK::NodePool", "kubernetes", "node_pool"}, {"ACS::ROS::Stack", "other", "resource"}, {"ACS::SLB::AccessControlList", "network", "network"}, {"ACS::Ga::Accelerator", "network", "network"}, {"ACS::CBWP::CommonBandwidthPackage", "network", "network"}, {"ACS::Redis::DBInstance", "database", "cache"}, {"ACS::RDS::DBInstance", "database", "database"}, {"ACS::PolarDB::DBCluster", "database", "database"}, {"ACS::ECS::Instance", "compute", "instance"}, {"ACS::ECS::Snapshot", "other", "resource"}, {"ACS::ECS::Disk", "compute", "disk"}, {"ACS::ECS::SecurityGroup", "network", "security_group"}, {"ACS::SLB::LoadBalancer", "network", "load_balancer"}, {"ACS::OSS::Bucket", "storage", "bucket"}, {"ACS::OSS::BucketPolicy", "other", "resource"}, {"ACS::RAM::Policy", "iam", "policy"}, {"ACS::Unknown::Cluster", "other", "resource"}, {"ACS::RDS::ParameterGroup", "other", "resource"}} {
		t.Run(tc.nativeType, func(t *testing.T) {
			domain, kind := classifyAlibabaInventory(tc.nativeType)
			if domain != tc.domain || kind != tc.kind {
				t.Fatalf("got %s/%s want %s/%s", domain, kind, tc.domain, tc.kind)
			}
		})
	}
}
