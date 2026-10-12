package providers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestPodProjectionDropsEnvironmentSecretsAndReturnsContinuation(t *testing.T) {
	fixture := `{"metadata":{},"items":[{"metadata":{"name":"pod","namespace":"system","uid":"uid","annotations":{"password":"hidden"}},"spec":{"nodeName":"node","containers":[{"env":[{"name":"TOKEN","value":"hidden"}],"envFrom":[{"secretRef":{"name":"hidden"}}]}]},"status":{"phase":"Running","conditions":[{"type":"Ready","status":"True"}],"containerStatuses":[{"ready":true,"restartCount":2,"state":{"waiting":{"message":"hidden"}}}]}}]}`
	for _, cloud := range []model.Provider{model.ProviderAzure, model.ProviderGCP} {
		page, err := decodeLivePods(strings.NewReader(fixture), cloud, "profile", "scope", "region", "cluster", provider.NativeRequest{})
		if err != nil || len(page.Rows) != 1 {
			t.Fatalf("Pod projection: %v", err)
		}
		attrs := page.Rows[0]["attributes"].(map[string]any)
		if attrs["ready"] != "True" || attrs["ready_containers"] != 1 || attrs["restart_count"] != 2 {
			t.Fatal("Pod status lost")
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "hidden") || strings.Contains(string(raw), "envFrom") {
			t.Fatal("Pod configuration leaked")
		}
		if page, err := decodeLivePods(strings.NewReader(strings.Replace(fixture, `"metadata":{}`, `"metadata":{"continue":"next"}`, 1)), cloud, "profile", "scope", "region", "cluster", provider.NativeRequest{}); err != nil || page.NextToken == "" {
			t.Fatal("Pod continuation not returned")
		}
	}
}

func TestPodScopeRejectedBeforeCredentials(t *testing.T) {
	a := &azureAdapter{profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderAzure, "azure.kubernetes.pods.list")
	_, err := a.readLiveNodes(context.Background(), provider.NativeRequest{Params: map[string]any{"subscription_id": "foreign", "resource_group": "rg", "cluster": "cluster"}}, spec)
	if !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("Azure foreign scope: %v", err)
	}
	g := &gcpAdapter{profile: config.Profile{Scopes: config.Scopes{Projects: []string{"allowed"}}}}
	spec, _ = deepDetailFor(model.ProviderGCP, "gcp.kubernetes.pods.list")
	_, err = g.readLiveNodes(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "foreign", "location": "asia-southeast1-b", "cluster": "cluster"}}, spec)
	if !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("GCP foreign scope: %v", err)
	}
}
