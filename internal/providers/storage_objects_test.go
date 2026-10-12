package providers

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/provider"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestGCPObjectsVerifyOwnershipBeforeListingAndDropSecrets(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TEST_GCP_OBJECT_CREDENTIALS", path)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	foreign := true
	objectCalls := 0
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Host {
		case "oauth2.googleapis.com":
			return providerTestHTTPResponse(r, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		case "cloudresourcemanager.googleapis.com":
			return providerTestHTTPResponse(r, `{"projectNumber":"123"}`), nil
		case "storage.googleapis.com":
			if r.URL.Path == "/storage/v1/b/bucket" {
				if foreign {
					return providerTestHTTPResponse(r, `{"name":"bucket","projectNumber":"999","location":"ASIA-SOUTHEAST1"}`), nil
				}
				return providerTestHTTPResponse(r, `{"name":"bucket","projectNumber":"123","location":"ASIA-SOUTHEAST1"}`), nil
			}
			objectCalls++
			if r.URL.Path != "/storage/v1/b/bucket/o" || r.URL.Query().Get("pageToken") != "opaque+next" || r.URL.Query().Get("maxResults") != "1" || r.URL.Query().Get("projection") != "noAcl" {
				return nil, fmt.Errorf("unexpected object request")
			}
			return providerTestHTTPResponse(r, `{"items":[{"name":"nested/probe.txt","bucket":"bucket","size":"768","generation":"1234","metadata":{"password":"hidden"},"mediaLink":"hidden"}],"nextPageToken":"next2"}`), nil
		}
		return nil, fmt.Errorf("unexpected host")
	})
	a := &gcpAdapter{name: "test", profile: config.Profile{Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_TEST_GCP_OBJECT_CREDENTIALS"}}, Scopes: config.Scopes{Projects: []string{"project"}}}}
	spec, _ := deepDetailFor(model.ProviderGCP, "gcp.storage.objects.list")
	req := provider.NativeRequest{Params: map[string]any{"project_id": "project", "bucket": "bucket"}, Limit: 1, PageToken: "opaque+next"}
	_, err := a.readObjects(context.Background(), req, spec)
	if !hasProviderErrorCode(err, "invalid_scope") || objectCalls != 0 {
		t.Fatalf("foreign ownership: %v calls=%d", err, objectCalls)
	}
	foreign = false
	page, err := a.readObjects(context.Background(), req, spec)
	if err != nil || len(page.Rows) != 1 || page.NextToken != "next2" || page.Rows[0]["attributes"].(map[string]any)["size_bytes"] != uint64(768) {
		t.Fatalf("objects: %+v %v", page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "hidden") {
		t.Fatal("object metadata leaked")
	}
}

func TestObjectScopeDeniedBeforeCredentials(t *testing.T) {
	a := &azureAdapter{profile: config.Profile{Scopes: config.Scopes{Subscriptions: []string{"allowed"}}}}
	spec, _ := deepDetailFor(model.ProviderAzure, "azure.storage.blobs.list")
	_, err := a.readObjects(context.Background(), provider.NativeRequest{Params: map[string]any{"subscription_id": "foreign", "resource_group": "rg", "account": "account", "container": "validation"}}, spec)
	if !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("foreign subscription: %v", err)
	}
	g := &gcpAdapter{profile: config.Profile{Scopes: config.Scopes{Projects: []string{"allowed"}}}}
	spec, _ = deepDetailFor(model.ProviderGCP, "gcp.storage.objects.list")
	_, err = g.readObjects(context.Background(), provider.NativeRequest{Params: map[string]any{"project_id": "foreign", "bucket": "bucket"}}, spec)
	if !hasProviderErrorCode(err, "scope_not_allowed") {
		t.Fatalf("foreign project: %v", err)
	}
}

func TestBlobXMLPreservesSizeDropsArbitraryMetadata(t *testing.T) {
	var data blobList
	err := xml.Unmarshal([]byte(`<EnumerationResults><Blobs><Blob><Name>folder/probe.txt</Name><Properties><Content-Length>17</Content-Length><BlobType>BlockBlob</BlobType><Last-Modified>date</Last-Modified></Properties><Metadata><secret>hidden</secret></Metadata></Blob></Blobs><NextMarker>opaque-next</NextMarker></EnumerationResults>`), &data)
	if err != nil || len(data.Blobs) != 1 || data.Blobs[0].Properties.Size != 17 || data.Next != "opaque-next" {
		t.Fatalf("decode: %+v %v", data, err)
	}
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), "hidden") {
		t.Fatal("metadata leaked")
	}
}

func TestObjectContinuationBounded(t *testing.T) {
	spec, _ := deepDetailFor(model.ProviderGCP, "gcp.storage.objects.list")
	req := provider.NativeRequest{Params: map[string]any{"project_id": "project", "bucket": "bucket"}, PageToken: "opaque/+?next"}
	if err := validateObjectList(req, spec); err != nil {
		t.Fatal(err)
	}
	req.PageToken = strings.Repeat("x", 8193)
	if validateObjectList(req, spec) == nil {
		t.Fatal("oversized continuation accepted")
	}
	req.PageToken = "bad\r\n"
	if validateObjectList(req, spec) == nil {
		t.Fatal("control characters accepted")
	}
}
