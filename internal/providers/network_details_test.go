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

func TestNetworkTopologyDropsSecretsRecursively(t *testing.T) {
	input := map[string]any{"name": "lb", "password": "secret", "properties": map[string]any{"backendAddressPools": []any{map[string]any{"id": "pool", "clientSecret": "hidden"}}, "customData": "hidden"}, "metadata": map[string]any{"token": "hidden"}}
	encoded, _ := json.Marshal(safeTopology(input))
	text := string(encoded)
	if strings.Contains(text, "secret") || strings.Contains(text, "hidden") || !strings.Contains(text, "pool") {
		t.Fatal(text)
	}
}

func TestNetworkDetailsRejectForeignScopeBeforeCredentials(t *testing.T) {
	azure := &azureAdapter{name: "test", profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderAzure, "azure.network.load_balancers.get")
	_, err := azure.readNetworkDetail(context.Background(), provider.NativeRequest{Params: map[string]any{"subscription_id": "foreign", "resource_group": "rg", "name": "lb"}}, spec)
	if err == nil {
		t.Fatal("foreign subscription accepted")
	}
	gcp := &gcpAdapter{name: "test", profile: config.Profile{Scopes: config.Scopes{Projects: []string{"allowed"}}}}
	spec, _ = deepDetailFor(model.ProviderGCP, "gcp.compute.routers.get")
	_, err = gcp.readNetworkDetail(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "foreign", "location": "asia-southeast1", "name": "router"}}, spec)
	if err == nil {
		t.Fatal("foreign project accepted")
	}
}

func TestSecurityTopologyPreservesRulesWithoutSecrets(t *testing.T) {
	input := map[string]any{"properties": map[string]any{"securityRules": []any{map[string]any{"name": "http", "properties": map[string]any{"priority": float64(100), "destinationPortRange": "80", "sourceAddressPrefix": "*", "access": "Allow", "clientSecret": "hidden"}}}}, "allowed": []any{map[string]any{"IPProtocol": "tcp", "ports": []any{"80"}}}, "targetTags": []any{"http"}}
	encoded, _ := json.Marshal(safeTopology(input))
	for _, expected := range []string{"securityRules", "priority", "destinationPortRange", "sourceAddressPrefix", "allowed", "targetTags"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatal(string(encoded))
		}
	}
	if strings.Contains(string(encoded), "hidden") {
		t.Fatal(string(encoded))
	}
}
