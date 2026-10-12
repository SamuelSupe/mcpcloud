package providers

import (
	"context"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"testing"
)

func TestAzureLiveNodesRejectForeignSubscription(t *testing.T) {
	a := &azureAdapter{profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderAzure, "azure.kubernetes.nodes.list")
	_, err := a.readLiveNodes(context.Background(), provider.NativeRequest{Params: map[string]any{"subscription_id": "foreign", "resource_group": "rg", "cluster": "cluster"}}, spec)
	if err == nil {
		t.Fatal("foreign subscription accepted")
	}
}
