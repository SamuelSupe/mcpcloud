package providers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestPodPaginationOver500AndScopedContinuation(t *testing.T) {
	for _, cloud := range []model.Provider{model.ProviderAzure, model.ProviderGCP} {
		req := provider.NativeRequest{Operation: string(cloud) + ".kubernetes.pods.list", Region: "region", Params: map[string]any{"cluster": "cluster"}, Limit: 100}
		seen := map[string]bool{}
		for pageNumber := 0; pageNumber < 7; pageNumber++ {
			query, err := podQuery(req, "profile")
			if err != nil {
				t.Fatal(err)
			}
			values, _ := url.ParseQuery(query)
			if values.Get("limit") != "100" {
				t.Fatal("page limit lost")
			}
			if pageNumber > 0 && values.Get("continue") != fmt.Sprintf("next/+?%d", pageNumber) {
				t.Fatal("continuation changed")
			}
			items := []any{}
			count := 100
			if pageNumber == 6 {
				count = 1
			}
			for i := 0; i < count; i++ {
				items = append(items, map[string]any{"metadata": map[string]any{"name": fmt.Sprintf("pod-%d", pageNumber*100+i), "namespace": "system"}})
			}
			next := ""
			if pageNumber < 6 {
				next = fmt.Sprintf("next/+?%d", pageNumber+1)
			}
			body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"continue": next}, "items": items})
			page, err := decodeLivePods(strings.NewReader(string(body)), cloud, "profile", "scope", "region", "cluster", req)
			if err != nil || len(page.Rows) != count {
				t.Fatalf("page: %v", err)
			}
			for _, row := range page.Rows {
				id := row["id"].(string)
				if seen[id] {
					t.Fatal("duplicate Pod")
				}
				seen[id] = true
			}
			if next == "" && page.NextToken != "" {
				t.Fatal("last page has cursor")
			}
			req.PageToken = page.NextToken
		}
		if len(seen) != 601 {
			t.Fatalf("total=%d", len(seen))
		}
	}
}

func TestPodCursorRejectsChangedContextAndRepeatedProviderToken(t *testing.T) {
	req := provider.NativeRequest{Operation: "gcp.kubernetes.pods.list", Region: "region", Params: map[string]any{"project_id": "project", "cluster": "cluster", "location": "zone"}, Limit: 1}
	fixture := `{"metadata":{"continue":"next/+?"},"items":[]}`
	page, err := decodeLivePods(strings.NewReader(fixture), model.ProviderGCP, "profile", "project", "region", "cluster", req)
	if err != nil {
		t.Fatal(err)
	}
	req.PageToken = page.NextToken
	if _, err = podQuery(req, "foreign-profile"); !hasProviderErrorCode(err, "invalid_cursor") {
		t.Fatalf("profile binding: %v", err)
	}
	for _, mutate := range []func(*provider.NativeRequest){func(r *provider.NativeRequest) { r.Region = "other" }, func(r *provider.NativeRequest) { r.Operation = "gcp.kubernetes.nodes.list" }, func(r *provider.NativeRequest) { r.Limit = 2 }, func(r *provider.NativeRequest) { r.Params = map[string]any{"cluster": "other"} }} {
		changed := req
		mutate(&changed)
		if _, err := podQuery(changed, "profile"); !hasProviderErrorCode(err, "invalid_cursor") {
			t.Fatalf("changed binding: %v", err)
		}
	}
	if _, err = decodeLivePods(strings.NewReader(fixture), model.ProviderGCP, "profile", "project", "region", "cluster", req); err == nil {
		t.Fatal("repeated provider token accepted")
	}
	for _, token := range []string{"opaque", strings.Repeat("x", 8193)} {
		req.PageToken = token
		if _, err = podQuery(req, "profile"); !hasProviderErrorCode(err, "invalid_cursor") {
			t.Fatal("invalid cursor accepted")
		}
	}
}
