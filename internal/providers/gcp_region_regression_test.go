package providers

import "testing"

func TestGCPZoneRegionNormalization(t *testing.T) {
	for input, want := range map[string]string{"asia-southeast1-b": "asia-southeast1", "us-central1-a": "us-central1", "asia-southeast1": "asia-southeast1", "global": "global", "us": "us", "": ""} {
		if got := gcpRegionForLocation(input); got != want {
			t.Fatalf("%q got %q want %q", input, got, want)
		}
	}
}

func TestGCPProjectScopePreservesConfiguredID(t *testing.T) {
	if got := gcpProjectForScope("projects/my-project", "projects/123456"); got != "my-project" {
		t.Fatal(got)
	}
	if got := gcpProjectForScope("organizations/123", "projects/456"); got != "456" {
		t.Fatal(got)
	}
}
