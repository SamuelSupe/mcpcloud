package providers

import (
	"context"
	"encoding/json"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"strings"
	"testing"
)

func TestStorageMetadataDropsSecrets(t *testing.T) {
	b, _ := json.Marshal(storageMetadata(map[string]any{"name": "bucket", "privateKey": "hidden", "properties": map[string]any{"publicAccess": "None", "metadata": map[string]any{"token": "hidden"}}}))
	if strings.Contains(string(b), "hidden") || !strings.Contains(string(b), "publicAccess") {
		t.Fatal(string(b))
	}
}
func TestStorageRejectsForeignScope(t *testing.T) {
	a := &azureAdapter{profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderAzure, "azure.storage.blob_containers.get")
	_, err := a.readStorageDetail(context.Background(), provider.NativeRequest{Params: map[string]any{"subscription_id": "foreign", "resource_group": "rg", "account": "account", "container": "test"}}, spec)
	if err == nil {
		t.Fatal("foreign subscription accepted")
	}
	g := &gcpAdapter{profile: config.Profile{Scopes: config.Scopes{Projects: []string{"allowed"}}}}
	spec, _ = deepDetailFor(model.ProviderGCP, "gcp.storage.buckets.get")
	_, err = g.readStorageDetail(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "foreign", "bucket": "test"}}, spec)
	if err == nil {
		t.Fatal("foreign project accepted")
	}
}
