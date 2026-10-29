package providers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

func TestGCPCostCursorBindsQueryAndAllowsPageSizeChanges(t *testing.T) {
	path := t.TempDir() + "/credentials.json"
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test","client_secret":"test","refresh_token":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_GCP_COST_CREDENTIALS", path)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	lastPageSize := ""
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(r, `{"access_token":"test","token_type":"Bearer","expires_in":3600}`), nil
		}
		if r.URL.Host != "bigquery.googleapis.com" {
			return nil, fmt.Errorf("unexpected host")
		}
		calls++
		if r.Method == "POST" {
			return providerTestHTTPResponse(r, `{"jobComplete":false,"jobReference":{"jobId":"job-1","location":"US"}}`), nil
		}
		if !strings.HasSuffix(r.URL.Path, "/queries/job-1") {
			return nil, fmt.Errorf("unexpected continuation %s", r.URL)
		}
		lastPageSize = r.URL.Query().Get("maxResults")
		return providerTestHTTPResponse(r, `{"jobComplete":true,"jobReference":{"jobId":"job-1","location":"US"},"schema":{"fields":[]},"rows":[]}`), nil
	})
	a := &gcpAdapter{name: "costs", profile: config.Profile{Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "MCP_GCP_COST_CREDENTIALS"}}, Options: map[string]string{"billing_table": "billing-project.dataset.export", "billing_location": "US"}}}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	req := provider.QueryRequest{Start: &start, End: &end, Limit: 300}
	page, err := a.queryCosts(context.Background(), "projects/project-a", req)
	if err != nil || page.NextToken == "" {
		t.Fatalf("initial query: %+v %v", page, err)
	}
	req.PageToken = page.NextToken
	for _, field := range []string{"profile", "scope", "table", "billing_project", "location", "start", "end", "legacy"} {
		t.Run(field, func(t *testing.T) {
			changed := *a
			changed.profile.Options = map[string]string{}
			for k, v := range a.profile.Options {
				changed.profile.Options[k] = v
			}
			changedReq := req
			scope := "projects/project-a"
			switch field {
			case "profile":
				changed.name = "other"
			case "scope":
				scope = "projects/project-b"
			case "table":
				changed.profile.Options["billing_table"] = "billing-project.dataset.other"
			case "billing_project":
				changed.profile.Options["billing_project"] = "other-project"
			case "location":
				changed.profile.Options["billing_location"] = "EU"
			case "start":
				v := start.Add(time.Hour)
				changedReq.Start = &v
			case "end":
				v := end.Add(time.Hour)
				changedReq.End = &v
			case "legacy":
				changedReq.PageToken = encodeBigQueryCursor(bigQueryCursor{JobID: "job-1", Location: "US"})
			}
			before := calls
			if _, err := changed.queryCosts(context.Background(), scope, changedReq); !hasProviderErrorCode(err, "invalid_cursor") {
				t.Fatalf("changed query accepted: %v", err)
			}
			if calls != before {
				t.Fatal("invalid cursor reached BigQuery")
			}
		})
	}
	req.Limit = 1
	page, err = a.queryCosts(context.Background(), "projects/project-a", req)
	if err != nil || page.NextToken != "" || calls != 2 || lastPageSize != "1" {
		t.Fatalf("same query continuation: %+v %v calls=%d", page, err, calls)
	}
}
