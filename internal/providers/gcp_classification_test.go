package providers

import "testing"

func TestGCPExactAssetClassification(t *testing.T) {
	cases := []struct{ asset, domain, kind string }{
		{"compute.googleapis.com/Instance", "compute", "instance"},
		{"compute.googleapis.com/Disk", "compute", "disk"},
		{"compute.googleapis.com/Router", "network", "network"},
		{"compute.googleapis.com/BackendService", "network", "load_balancer"},
		{"compute.googleapis.com/BackendBucket", "network", "load_balancer"},
		{"compute.googleapis.com/ForwardingRule", "network", "load_balancer"},
		{"compute.googleapis.com/InstanceGroup", "other", "resource"},
		{"compute.googleapis.com/InstanceTemplate", "other", "resource"},
		{"storage.googleapis.com/Bucket", "storage", "bucket"},
		{"logging.googleapis.com/LogBucket", "logging", "log_group"},
		{"container.googleapis.com/Cluster", "kubernetes", "cluster"},
		{"k8s.io/Node", "other", "resource"},
		{"sqladmin.googleapis.com/Instance", "database", "database"},
		{"redis.googleapis.com/Instance", "database", "cache"},
	}
	for _, c := range cases {
		d, k := classifyGCP(c.asset)
		if d != c.domain || k != c.kind {
			t.Errorf("%s: got %s/%s want %s/%s", c.asset, d, k, c.domain, c.kind)
		}
	}
}
