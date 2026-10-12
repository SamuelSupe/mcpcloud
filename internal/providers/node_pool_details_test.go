package providers

import (
	"encoding/json"
	"mcpcloud/internal/model"
	"strings"
	"testing"
)

func TestNodePoolsExcludeCredentialsAndRemainConfiguration(t *testing.T) {
	page := poolRows(model.ProviderGCP, "test", "project", "region", "cluster", []map[string]any{{"name": "pool", "initialNodeCount": float64(1), "config": map[string]any{"machineType": "e2-medium", "oauthScopes": []any{"hidden"}, "metadata": map[string]any{"password": "hidden"}}}})
	b, _ := json.Marshal(page.Rows)
	if strings.Contains(string(b), "hidden") || !strings.Contains(string(b), "cloud_control_plane_configuration") || !strings.Contains(string(b), "e2-medium") {
		t.Fatal(string(b))
	}
	if page.Rows[0]["kind"] != "node_pool" {
		t.Fatal(page.Rows)
	}
}
