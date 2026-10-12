package provider

import (
	"reflect"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

func TestAzureIAMTargetsSubscriptionOnce(t *testing.T) {
	a := newScopedAdapter(registryAdapter{provider: model.ProviderAzure}, config.Profile{Regions: []string{"eastus2", "westus2"}}).(*scopedAdapter)
	for _, requested := range [][]string{nil, {"eastus2", "westus2"}} {
		got, err := a.TargetRegions(model.SourceIAM, requested)
		if err != nil || !reflect.DeepEqual(got, []string{""}) {
			t.Fatalf("IAM targets = %v, %v; want one global target", got, err)
		}
	}
	got, err := a.TargetRegions(model.SourceResources, nil)
	if err != nil || !reflect.DeepEqual(got, []string{"eastus2", "westus2"}) {
		t.Fatalf("resource targets = %v, %v; want configured regions", got, err)
	}
	if _, err := a.TargetRegions(model.SourceIAM, []string{"unconfigured"}); err == nil {
		t.Fatal("IAM accepted a region outside the profile")
	}
}
