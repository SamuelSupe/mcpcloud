package providers

import (
	"testing"
	"time"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestAzureExactProductClassification(t *testing.T) {
	tests := []struct{ typ, domain, kind string }{
		{"Microsoft.Compute/virtualMachines", "compute", "instance"},
		{"Microsoft.Compute/disks", "compute", "disk"},
		{"Microsoft.Compute/virtualMachines/extensions", "other", "resource"},
		{"Microsoft.Compute/virtualMachineScaleSets", "other", "resource"},
		{"Microsoft.OperationalInsights/workspaces", "logging", "log_project"},
		{"Microsoft.CognitiveServices/accounts", "other", "resource"},
		{"Microsoft.CognitiveServices/accounts/projects", "other", "resource"},
		{"Microsoft.Insights/metricAlerts", "monitoring", "alarm"},
		{"Microsoft.Authorization/roleAssignments", "iam", "binding"},
		{"Microsoft.Cache/redisEnterprise", "database", "cache"},
		{"Microsoft.Storage/storageAccounts", "storage", "bucket"},
		{"Microsoft.Storage/storageAccounts/blobServices/containers", "storage", "container"},
	}
	for _, tt := range tests {
		row := baseRow(model.ProviderAzure, "test", "id", "name", "misleading-monitor-service", tt.typ, "eastus2", "sub", time.Now())
		if row["domain"] != tt.domain || row["kind"] != tt.kind {
			t.Fatalf("%s classified as %v/%v", tt.typ, row["domain"], row["kind"])
		}
	}
}

func TestAzureListsExcludeUnrelatedNamesAndWorkspaceIsLogging(t *testing.T) {
	rows := []map[string]any{}
	for _, typ := range []string{"Microsoft.Compute/virtualMachines", "Microsoft.Compute/virtualMachineScaleSets", "Microsoft.OperationalInsights/workspaces", "Microsoft.CognitiveServices/accounts"} {
		rows = append(rows, baseRow(model.ProviderAzure, "test", "id", "alarm-looking-name", "namespace", typ, "eastus2", "sub", time.Now()))
	}
	for _, test := range []struct {
		operation, typ string
		count          int
	}{
		{"azure.compute.list_instances", "Microsoft.Compute/virtualMachines", 1},
		{"azure.logging.list_resources", "Microsoft.OperationalInsights/workspaces", 1},
		{"azure.monitoring.list_alarms", "", 0},
	} {
		spec, _ := nativeProductFor(model.ProviderAzure, test.operation)
		copyRows := append([]map[string]any(nil), rows...)
		page, err := completeNativeProduct(provider.Page{Rows: copyRows}, nil, spec, test.operation)
		if err != nil || len(page.Rows) != test.count {
			t.Fatalf("%s: %+v %v", test.operation, page, err)
		}
		if test.count > 0 && page.Rows[0]["native"].(map[string]any)["resource_type"] != test.typ {
			t.Fatal("unrelated product returned")
		}
	}
}
