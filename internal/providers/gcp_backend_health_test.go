package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
)

func TestBackendHealthFixedRequestsAndScope(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test","client_secret":"test","refresh_token":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TEST_HEALTH_CREDENTIALS", path)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	group := "https://www.googleapis.com/compute/v1/projects/project/zones/asia-southeast1-b/instanceGroups/group"
	foreign, healthCalls := false, 0
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(r, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if r.URL.Host != "compute.googleapis.com" {
			return nil, fmt.Errorf("unexpected host")
		}
		if strings.HasSuffix(r.URL.Path, "/getHealth") {
			healthCalls++
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return nil, err
			}
			if r.Method != "POST" || body["group"] != group {
				return nil, fmt.Errorf("invalid health request")
			}
			return providerTestHTTPResponse(r, `{"healthStatus":[{"instance":"vm","ipAddress":"10.0.0.2","port":80,"healthState":"HEALTHY","annotations":{"secret":"hidden"}}],"annotations":{"secret":"hidden"}}`), nil
		}
		if r.Method != "GET" || (r.URL.Path != "/compute/v1/projects/project/regions/asia-southeast1/backendServices/backend" && r.URL.Path != "/compute/v1/projects/project/global/backendServices/backend") {
			return nil, fmt.Errorf("unexpected backend request")
		}
		ref := group
		if foreign {
			ref = strings.Replace(group, "/projects/project/", "/projects/foreign/", 1)
		}
		return providerTestHTTPResponse(r, fmt.Sprintf(`{"name":"backend","selfLink":%q,"backends":[{"group":%q},{"group":%q}],"description":"hidden"}`, "https://www.googleapis.com"+r.URL.Path, ref, ref)), nil
	})
	a := &gcpAdapter{name: "test", profile: config.Profile{Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_TEST_HEALTH_CREDENTIALS"}}, Scopes: config.Scopes{Projects: []string{"project"}}}}
	globalSpec, _ := deepDetailFor(model.ProviderGCP, "gcp.compute.global_backend_services.get")
	globalPage, globalErr := a.readNetworkDetail(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "project", "name": "backend"}}, globalSpec)
	if globalErr != nil || len(globalPage.Rows) != 1 || globalPage.Rows[0]["region"] != "global" || globalPage.Rows[0]["id"] == "" {
		t.Fatalf("global backend detail: %+v %v", globalPage, globalErr)
	}
	globalJSON, _ := json.Marshal(globalPage)
	if strings.Contains(string(globalJSON), "hidden") {
		t.Fatal("global backend description leaked")
	}
	for _, op := range []string{"gcp.compute.backend_services.get_health", "gcp.compute.global_backend_services.get_health"} {
		spec, _ := deepDetailFor(model.ProviderGCP, op)
		params := map[string]any{"project_id": "project", "name": "backend"}
		if op == "gcp.compute.backend_services.get_health" {
			params["location"] = "asia-southeast1"
		}
		page, err := a.readBackendHealth(context.Background(), provider.NativeRequest{Params: params}, spec)
		if err != nil || len(page.Rows) != 1 || page.Requests != 2 {
			t.Fatalf("health: %+v %v", page, err)
		}
		if page.Rows[0]["attributes"].(map[string]any)["health_state"] != "HEALTHY" {
			t.Fatal("missing health state")
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "hidden") {
			t.Fatal("annotations leaked")
		}
		foreign = true
		before := healthCalls
		_, err = a.readBackendHealth(context.Background(), provider.NativeRequest{Params: params}, spec)
		if !hasProviderErrorCode(err, "invalid_scope") || before != healthCalls {
			t.Fatalf("foreign group: %v", err)
		}
		foreign = false
		params["project_id"] = "foreign"
		_, err = a.readBackendHealth(context.Background(), provider.NativeRequest{Params: params}, spec)
		if !hasProviderErrorCode(err, "scope_not_allowed") {
			t.Fatalf("foreign project: %v", err)
		}
	}
}

func TestBackendGroupRejectsURLsOutsideFixedScope(t *testing.T) {
	base := "https://www.googleapis.com/compute/v1/projects/project/zones/asia-southeast1-b/instanceGroups/group"
	if !gcpBackendGroupValid(base, "project") {
		t.Fatal("valid group rejected")
	}
	for _, raw := range []string{strings.Replace(base, "https:", "http:", 1), base + "?url=other", base + "#fragment", strings.Replace(base, "www.googleapis.com", "evil.example", 1), strings.Replace(base, "/group", "/..", 1), strings.Replace(base, "/group", "/%2e%2e", 1), strings.Replace(base, "/projects/project/", "/projects/foreign/", 1)} {
		if gcpBackendGroupValid(raw, "project") {
			t.Fatalf("unsafe group accepted: %s", raw)
		}
	}
}
