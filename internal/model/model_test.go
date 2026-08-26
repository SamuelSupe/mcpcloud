package model

import (
	"reflect"
	"testing"
	"time"
)

func TestRecordRowIncludesCanonicalFieldsAndUTCInstants(t *testing.T) {
	zone := time.FixedZone("test+08", 8*60*60)
	createdAt := time.Date(2024, time.January, 2, 10, 11, 12, 345_000_000, zone)
	updatedAt := time.Date(2024, time.February, 3, 4, 5, 6, 7, time.FixedZone("test-05", -5*60*60))
	observedAt := time.Date(2024, time.March, 4, 5, 6, 7, 123_456_789, zone)
	record := Record{
		Provider: ProviderAWS,
		Profile:  "production",
		Domain:   "resources",
		Service:  "compute",
		Kind:     "instance",
		ID:       "i-123",
		Name:     "web-1",
		Scope: Scope{
			OrganizationID:  "org-1",
			TenantID:        "tenant-1",
			AccountID:       "account-1",
			ProjectID:       "project-1",
			SubscriptionID:  "subscription-1",
			ResourceGroupID: "resource-group-1",
		},
		Region:     "ap-southeast-1",
		Zone:       "ap-southeast-1a",
		State:      "running",
		Tags:       map[string]any{"env": "prod", "tier": "web"},
		CreatedAt:  &createdAt,
		UpdatedAt:  &updatedAt,
		ObservedAt: observedAt,
		Attributes: map[string]any{"vcpus": 4},
		Native:     map[string]any{"instance_type": "t3.small"},
	}

	want := map[string]any{
		"provider": "aws",
		"profile":  "production",
		"domain":   "resources",
		"service":  "compute",
		"kind":     "instance",
		"id":       "i-123",
		"name":     "web-1",
		"scope": map[string]any{
			"organization_id":   "org-1",
			"tenant_id":         "tenant-1",
			"account_id":        "account-1",
			"project_id":        "project-1",
			"subscription_id":   "subscription-1",
			"resource_group_id": "resource-group-1",
		},
		"region":      "ap-southeast-1",
		"zone":        "ap-southeast-1a",
		"state":       "running",
		"tags":        record.Tags,
		"created_at":  "2024-01-02T02:11:12.345Z",
		"updated_at":  "2024-02-03T09:05:06.000000007Z",
		"observed_at": "2024-03-03T21:06:07.123456789Z",
		"attributes":  record.Attributes,
		"native":      record.Native,
	}

	got := record.Row(true)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Record.Row(true) = %#v, want %#v", got, want)
	}
}

func TestRecordRowOmitsOptionalValuesWhenUnsetOrExcluded(t *testing.T) {
	record := Record{
		Provider:   ProviderGCP,
		ID:         "resource-1",
		ObservedAt: time.Date(2024, time.April, 5, 6, 7, 8, 0, time.UTC),
		Native:     map[string]any{"id": "native-1"},
	}

	withoutNative := record.Row(false)
	for _, key := range []string{"created_at", "updated_at", "native"} {
		if _, ok := withoutNative[key]; ok {
			t.Errorf("Record.Row(false) included optional key %q", key)
		}
	}
	for _, key := range []string{"name", "region", "zone", "state", "tags", "attributes", "observed_at"} {
		if _, ok := withoutNative[key]; !ok {
			t.Errorf("Record.Row(false) omitted canonical key %q", key)
		}
	}

	withNative := record.Row(true)
	if got := withNative["native"]; !reflect.DeepEqual(got, record.Native) {
		t.Fatalf("Record.Row(true)[native] = %#v, want %#v", got, record.Native)
	}
	if got := withoutNative["observed_at"]; got != "2024-04-05T06:07:08Z" {
		t.Fatalf("Record.Row(false)[observed_at] = %#v, want UTC RFC3339Nano value", got)
	}
}
