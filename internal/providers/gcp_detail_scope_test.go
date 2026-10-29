package providers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestGCPFolderDetailsVerifyAncestryBeforeReading(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test","client_secret":"test","refresh_token":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_GCP_FOLDER_CREDENTIALS", path)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	folder, detailCalls, ancestryCalls := "123", 0, 0
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(r, `{"access_token":"test","token_type":"Bearer","expires_in":3600}`), nil
		}
		if r.URL.Host == "cloudresourcemanager.googleapis.com" {
			ancestryCalls++
			if r.Method != "POST" || r.URL.Path != "/v1beta1/projects/project-a:getAncestry" {
				return nil, fmt.Errorf("unexpected ancestry request: %s %s", r.Method, r.URL.Path)
			}
			return providerTestHTTPResponse(r, fmt.Sprintf(`{"ancestor":[{"resourceId":{"type":"project","id":"456"}},{"resourceId":{"type":"folder","id":"child"}},{"resourceId":{"type":"folder","id":%q}},{"resourceId":{"type":"organization","id":"999"}}]}`, folder)), nil
		}
		detailCalls++
		switch {
		case r.URL.Host == "compute.googleapis.com" && strings.HasSuffix(r.URL.Path, "/instances/vm"):
			return providerTestHTTPResponse(r, `{"id":"1","name":"vm","zone":"us-central1-a"}`), nil
		case r.URL.Host == "container.googleapis.com" && strings.HasSuffix(r.URL.Path, "/clusters/cluster"):
			return providerTestHTTPResponse(r, `{"name":"cluster","location":"us-central1","zone":"us-central1"}`), nil
		case r.URL.Host == "compute.googleapis.com" && strings.HasSuffix(r.URL.Path, "/firewalls/fw"):
			return providerTestHTTPResponse(r, `{"id":"2","name":"fw","selfLink":"https://www.googleapis.com/compute/v1/projects/project-a/global/firewalls/fw"}`), nil
		default:
			return nil, fmt.Errorf("unexpected detail: %s", r.URL)
		}
	})
	profile := config.Profile{Provider: model.ProviderGCP, Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_GCP_FOLDER_CREDENTIALS"}}, Options: map[string]string{"asset_scope": "folders/123"}, Regions: []string{"us-central1"}}
	registry, err := provider.FromConfig(config.Config{Profiles: map[string]config.Profile{"folder": profile}})
	if err != nil {
		t.Fatal(err)
	}
	adapter, _ := registry.Get("folder")
	requests := []provider.NativeRequest{
		{Operation: "gcp.compute.instances.get", Params: map[string]any{"project_id": "project-a", "zone": "us-central1-a", "instance": "vm"}},
		{Operation: "gcp.container.clusters.get", Params: map[string]any{"project_id": "project-a", "location": "us-central1", "cluster": "cluster"}},
		{Operation: "gcp.compute.firewalls.get", Params: map[string]any{"project_id": "project-a", "name": "fw"}},
	}
	for _, req := range requests {
		page, err := adapter.NativeRead(context.Background(), req)
		if err != nil || len(page.Rows) != 1 || page.Requests != 2 {
			t.Fatalf("%s: %+v %v", req.Operation, page, err)
		}
		if !stringIn(page.Rows[0]["scope"].(map[string]any)["folder_ids"].([]string), "folders/123") {
			t.Fatal("missing verified folder ancestry")
		}
	}
	folder = "outside"
	before := detailCalls
	for _, req := range requests {
		if _, err := adapter.NativeRead(context.Background(), req); !hasProviderErrorCode(err, "scope_not_allowed") {
			t.Fatalf("foreign project: %v", err)
		}
	}
	if detailCalls != before || ancestryCalls != 6 {
		t.Fatalf("unauthorized details were read: details=%d ancestry=%d", detailCalls, ancestryCalls)
	}
}
