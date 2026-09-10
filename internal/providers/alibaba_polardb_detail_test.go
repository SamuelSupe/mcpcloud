package providers

import (
	"context"
	"encoding/json"
	"github.com/aliyun/alibaba-cloud-sdk-go/services/polardb"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

type polarDetailStub struct {
	response *polardb.DescribeDBClusterAttributeResponse
	request  *polardb.DescribeDBClusterAttributeRequest
}

func (s *polarDetailStub) DescribeDBClusterAttribute(r *polardb.DescribeDBClusterAttributeRequest) (*polardb.DescribeDBClusterAttributeResponse, error) {
	s.request = r
	return s.response, nil
}
func TestAlibabaPolarDBDetailScopedAndSafe(t *testing.T) {
	old := newAlibabaPolarDBDetailClient
	t.Cleanup(func() { newAlibabaPolarDBDetailClient = old })
	p := polardb.CreateDescribeDBClusterAttributeResponse()
	p.DBClusterId = "pc-test"
	p.RegionId = "cn-hangzhou"
	p.DBType = "PostgreSQL"
	p.DBVersion = "16"
	p.VPCId = "forbidden-vpc"
	p.VSwitchId = "forbidden-subnet"
	p.DBNodes = []polardb.DBNode{{DBNodeId: "pi-test", DBNodeRole: "Writer", DBNodeStatus: "Running", DBNodeClass: "test-class", MirrorInsName: "forbidden-mirror"}}
	stub := &polarDetailStub{response: p}
	newAlibabaPolarDBDetailClient = func(_ config.Profile, region string) (alibabaPolarDBDetailAPI, error) {
		if region != "cn-hangzhou" {
			t.Fatal(region)
		}
		return stub, nil
	}
	a := &alibabaAdapter{name: "test", profile: config.Profile{Scopes: config.Scopes{Accounts: []string{"123"}}, Regions: []string{"cn-hangzhou"}}}
	r := provider.NativeRequest{Operation: alibabaPolarDBDetailOperation, Region: "cn-hangzhou", Params: map[string]any{"db_cluster_id": "pc-test"}}
	page, err := a.NativeRead(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 || stub.request.DBClusterId != "pc-test" || stub.request.OwnerAccount != "" || page.Requests != 1 {
		t.Fatalf("unexpected response %+v", page)
	}
	attrs := page.Rows[0]["attributes"].(map[string]any)
	if attrs["engine"] != "postgresql" || attrs["engine_version"] != "16" || page.Rows[0]["name"] != "pc-test" {
		t.Fatal(attrs)
	}
	nodes := attrs["nodes"].([]map[string]any)
	if len(nodes) != 1 || nodes[0]["id"] != "pi-test" || nodes[0]["role"] != "Writer" {
		t.Fatal(nodes)
	}
	b, _ := json.Marshal(page.Rows)
	if strings.Contains(string(b), "forbidden-") {
		t.Fatal("sensitive data leaked")
	}
	for _, bad := range []provider.NativeRequest{{Operation: r.Operation, Region: "cn-shanghai", Params: r.Params}, {Operation: r.Operation, Region: r.Region, Params: r.Params, PageToken: "cursor"}, {Operation: r.Operation, Region: r.Region, Params: map[string]any{"db_cluster_id": "pc-test", "endpoint": "https://example.com"}}, {Operation: r.Operation, Region: r.Region, Params: map[string]any{"db_cluster_id": "pc-test", "db_node_id": "pi-other"}}} {
		stub.request = nil
		if _, err := a.NativeRead(context.Background(), bad); err == nil || stub.request != nil {
			t.Fatal("invalid request reached SDK")
		}
	}
	a.profile.Scopes.Accounts = []string{"123", "456"}
	stub.request = nil
	if _, err := a.NativeRead(context.Background(), r); err == nil || stub.request != nil {
		t.Fatal("multiple accounts accepted")
	}
	a.profile.Scopes.Accounts = []string{"123"}
	for _, change := range []func(){func() { p.DBClusterId = "other" }, func() { p.DBClusterId = "pc-test"; p.RegionId = "other" }, func() { p.RegionId = "cn-hangzhou"; p.DBNodes[0].RegionId = "other" }, func() { p.DBNodes[0].RegionId = ""; p.DBNodes[0].DBNodeId = "" }, func() { p.DBNodes[0].DBNodeId = "pi-test"; p.DBNodes = append(p.DBNodes, p.DBNodes[0]) }, func() { stub.response = nil }} {
		change()
		if _, err := a.NativeRead(context.Background(), r); err == nil {
			t.Fatal("invalid provider response accepted")
		}
	}
	stub.request = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.NativeRead(ctx, r); err == nil || stub.request != nil {
		t.Fatal("cancelled request reached SDK")
	}
}
