package providers

import (
	"context"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"testing"
)

func TestLiveNodeRowUsesActualReadyCondition(t *testing.T) {
	var n liveNode
	n.Metadata.Name = "actual-node"
	n.Status.NodeInfo.KubeletVersion = "v1.36"
	n.Status.Conditions = append(n.Status.Conditions, struct {
		Type   string `json:"type"`
		Status string `json:"status"`
	}{"Ready", "True"})
	page := liveNodeRows("test", "project", "region", "cluster", []liveNode{n})
	row := page.Rows[0]
	attrs := row["attributes"].(map[string]any)
	if row["kind"] != "node" || attrs["ready"] != "True" || attrs["data_source"] != "kubernetes_api" {
		t.Fatal(row)
	}
}
func TestLiveNodesRejectForeignProjectBeforeCredentials(t *testing.T) {
	a := &gcpAdapter{profile: config.Profile{Scopes: config.Scopes{Projects: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderGCP, "gcp.kubernetes.nodes.list")
	_, err := a.readLiveNodes(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "foreign", "location": "region-a", "cluster": "cluster"}}, spec)
	if err == nil {
		t.Fatal("foreign project accepted")
	}
}
