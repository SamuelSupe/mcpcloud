package providers

import (
	"mcpcloud/internal/model"
	"testing"
	"time"
)

func TestAzureDisplayRegionNormalized(t *testing.T) {
	for input, want := range map[string]string{"Southeast Asia": "southeastasia", "West US 2": "westus2", "eastus2": "eastus2"} {
		row := baseRow(model.ProviderAzure, "test", "id", "name", "db", "db", input, "sub", time.Now())
		if row["region"] != want {
			t.Fatalf("region %q: got %v want %s", input, row["region"], want)
		}
	}
	row := baseRow(model.ProviderGCP, "test", "id", "name", "db", "db", "asia-southeast1", "project", time.Now())
	if row["region"] != "asia-southeast1" {
		t.Fatal("GCP region changed")
	}
}
