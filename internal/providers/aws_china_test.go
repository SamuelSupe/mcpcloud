package providers

import (
	"testing"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestAWSCostExplorerRegion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		regions []string
		want    string
	}{
		{name: "commercial", regions: []string{"ap-southeast-1"}, want: "us-east-1"},
		{name: "china ningxia", regions: []string{"cn-northwest-1"}, want: "cn-northwest-1"},
		{name: "china beijing", regions: []string{"cn-north-1"}, want: "cn-northwest-1"},
		{name: "china after wildcard", regions: []string{"*", "cn-north-1"}, want: "cn-northwest-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := awsCostExplorerRegion(tc.regions); got != tc.want {
				t.Fatalf("awsCostExplorerRegion(%v) = %q, want %q", tc.regions, got, tc.want)
			}
		})
	}
}

func TestAWSNativeProductTypeBoundaries(t *testing.T) {
	instanceSpec, ok := nativeProductFor(model.ProviderAWS, "aws.compute.list_instances")
	if !ok {
		t.Fatal("missing AWS instance product operation")
	}
	page, err := completeNativeProduct(provider.Page{Rows: []map[string]any{
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "AWS::EC2::Instance"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "ec2:instance"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "AWS::EC2::EC2Fleet"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "ec2:snapshot"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "ec2:security-group"}},
	}}, nil, instanceSpec, "aws.compute.list_instances")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 {
		t.Fatalf("AWS instance rows = %d, want 2", len(page.Rows))
	}

	clusterSpec, ok := nativeProductFor(model.ProviderAWS, "aws.kubernetes.list_resources")
	if !ok {
		t.Fatal("missing AWS Kubernetes product operation")
	}
	page, err = completeNativeProduct(provider.Page{Rows: []map[string]any{
		{"domain": "kubernetes", "kind": "cluster", "native": map[string]any{"resource_type": "AWS::EKS::Cluster"}},
		{"domain": "kubernetes", "kind": "cluster", "native": map[string]any{"resource_type": "eks:deployment"}},
		{"domain": "kubernetes", "kind": "cluster", "native": map[string]any{"resource_type": "eks:replicaset"}},
	}}, nil, clusterSpec, "aws.kubernetes.list_resources")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("AWS Kubernetes rows = %d, want 1", len(page.Rows))
	}
}

func TestAWSMetricUnits(t *testing.T) {
	for _, tc := range []struct {
		namespace string
		metric    string
		want      string
	}{
		{namespace: "AWS/EC2", metric: "CPUUtilization", want: "%"},
		{namespace: "AWS/EC2", metric: "NetworkIn", want: "Bytes"},
		{namespace: "AWS/RDS", metric: "ReadIOPS", want: "Count/Second"},
		{namespace: "Custom/App", metric: "CPUUtilization", want: ""},
	} {
		if got := awsMetricUnit(tc.namespace, tc.metric); got != tc.want {
			t.Fatalf("awsMetricUnit(%q, %q) = %q, want %q", tc.namespace, tc.metric, got, tc.want)
		}
	}
}
