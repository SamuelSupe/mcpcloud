package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestAWSIAMQueryPushesServiceFilter(t *testing.T) {
	queries := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ QueryString string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		queries <- body.QueryString
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Resources":[{"Arn":"arn:aws:iam::123456789012:role/test","Service":"iam","ResourceType":"iam:role","Region":"global","OwningAccountId":"123456789012"}]}`))
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL_RESOURCE_EXPLORER_2", server.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	a := &awsAdapter{name: "iam", profile: envCredentialProfile(nil)}
	a.profile.Scopes.Accounts = []string{"123456789012"}
	a.profile.Regions = []string{"us-east-1"}
	for _, source := range []model.Source{model.SourceIAM, model.SourceResources} {
		page, err := a.Query(context.Background(), provider.QueryRequest{Source: source, Limit: 1})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("query: %+v %v", page, err)
		}
		query := <-queries
		if strings.Contains(query, "service:iam") != (source == model.SourceIAM) || !strings.Contains(query, "accountid:123456789012") {
			t.Fatalf("wrong provider query: %q", query)
		}
	}
	page, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: "aws.iam.list_resources", Limit: 1})
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("native IAM read: %+v %v", page, err)
	}
	if query := <-queries; !strings.Contains(query, "service:iam") || !strings.Contains(query, "accountid:123456789012") {
		t.Fatalf("wrong native IAM query: %q", query)
	}
}
