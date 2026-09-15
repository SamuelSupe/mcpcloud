package providers

import (
	"encoding/json"
	"reflect"
	"testing"

	"mcpcloud/internal/provider"
)

func TestAlibabaResourceCenterUsesGlobalEndpoint(t *testing.T) {
	request := newAlibabaSearchResourcesRequest()
	if got, want := request.GetDomain(), "resourcecenter.aliyuncs.com"; got != want {
		t.Fatalf("Resource Center domain = %q, want %q", got, want)
	}
}

func TestAlibabaTagFilterEscapesValuesAndAllowsKeyOrValue(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		want             map[string]string
	}{
		{name: "key and value", key: "environment", value: "prod\"blue", want: map[string]string{"key": "environment", "value": "prod\"blue"}},
		{name: "key only", key: "owner", want: map[string]string{"key": "owner"}},
		{name: "value only", value: "shared", want: map[string]string{"value": "shared"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, ok := alibabaTagFilter(test.key, test.value)
			if !ok {
				t.Fatal("tag filter unexpectedly omitted")
			}
			var got map[string]string
			if err := json.Unmarshal([]byte(encoded), &got); err != nil {
				t.Fatalf("tag filter is not JSON: %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("tag filter = %#v, want %#v", got, test.want)
			}
		})
	}
	if _, ok := alibabaTagFilter("", ""); ok {
		t.Fatal("empty tag filter should be omitted")
	}
}

func TestAlibabaACKTagsAcceptObjectAndArray(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "object", body: `{"tags":{"environment":"sit"}}`},
		{name: "array", body: `{"tags":[{"key":"environment","value":"sit"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var detail alibabaACKDetailResponse
			if err := json.Unmarshal([]byte(test.body), &detail); err != nil {
				t.Fatal(err)
			}
			if got, want := map[string]string(detail.Tags), map[string]string{"environment": "sit"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("tags = %#v, want %#v", got, want)
			}
		})
	}
}

func TestAlibabaInstanceInventoryExcludesSnapshots(t *testing.T) {
	spec, ok := nativeProductFor("alibaba", "alibaba.compute.list_instances")
	if !ok {
		t.Fatal("Alibaba instance inventory operation is not registered")
	}
	page, err := completeNativeProduct(provider.Page{Rows: []map[string]any{
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "ACS::ECS::Instance"}},
		{"domain": "compute", "kind": "instance", "native": map[string]any{"resource_type": "ACS::ECS::Snapshot"}},
	}}, nil, spec, "alibaba.compute.list_instances")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(page.Rows), 1; got != want {
		t.Fatalf("filtered rows = %d, want %d", got, want)
	}
}
