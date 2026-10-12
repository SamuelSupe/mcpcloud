package providers

import "testing"

func TestAzureResourceMetricsScopeBoundary(t *testing.T) {
	id := "/subscriptions/sub-a/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb"
	if path, err := azureMetricResourcePath("sub-a", "Microsoft.Network/loadBalancers", id); err != nil || path != id {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, bad := range []string{
		"https://evil.example" + id,
		"/subscriptions/sub-b/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb",
		id + "?query=x", id + "%2f..", id + "/../name",
		"/subscriptions/sub-a/resourceGroups/../providers/Microsoft.Network/loadBalancers/lb",
	} {
		if _, err := azureMetricResourcePath("sub-a", "Microsoft.Network/loadBalancers", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := azureMetricResourcePath("sub-a", "Microsoft.Compute/virtualMachines", id); err == nil {
		t.Fatal("accepted wrong namespace")
	}
}
