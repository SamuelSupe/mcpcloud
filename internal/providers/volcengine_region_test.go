package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

func TestVolcengineNativeInventoryRegion(t *testing.T) {
	t.Setenv("MCP_TEST_VOLC_ACCESS", "access")
	t.Setenv("MCP_TEST_VOLC_SECRET", "secret")
	adapter := &volcengineAdapter{name: "test", profile: config.Profile{
		Credential: config.Credential{Source: "env", Env: map[string]string{
			"VOLCENGINE_ACCESS_KEY_ID": "MCP_TEST_VOLC_ACCESS", "VOLCENGINE_SECRET_ACCESS_KEY": "MCP_TEST_VOLC_SECRET",
		}}, Regions: []string{"cn-beijing", "cn-shanghai"},
	}}
	previous := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = previous })
	var bodies []map[string]any
	http.DefaultClient = &http.Client{Transport: providerTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		return providerTestHTTPResponse(req, `{"Result":{"Resources":[],"NextToken":"next-page"}}`), nil
	})}
	for _, tc := range []struct {
		name, region, param, want string
		reject                    bool
	}{
		{name: "top-level", region: "cn-shanghai", want: "cn-shanghai"},
		{name: "legacy-param", param: "cn-beijing", want: "cn-beijing"},
		{name: "matching", region: "cn-shanghai", param: "cn-shanghai", want: "cn-shanghai"},
		{name: "conflicting", region: "cn-shanghai", param: "cn-beijing", reject: true},
		{name: "unspecified"},
		{name: "wildcard", region: "*", param: "cn-beijing", want: "cn-beijing"},
	} {
		for _, op := range adapter.Operations() {
			if op.Service != "resourcecenter" {
				continue
			}
			t.Run(tc.name+"/"+op.Name, func(t *testing.T) {
				bodies = nil
				params := map[string]any{}
				if tc.param != "" {
					params["region"] = tc.param
				}
				page, err := adapter.NativeRead(context.Background(), provider.NativeRequest{Operation: op.Name, Region: tc.region, Params: params, PageToken: "prior-page", Limit: 7})
				if tc.reject {
					if !hasProviderErrorCode(err, "invalid_parameter") || len(bodies) != 0 {
						t.Fatalf("conflict must fail before HTTP: err=%v requests=%d", err, len(bodies))
					}
					return
				}
				if err != nil || len(bodies) != 1 {
					t.Fatalf("read: err=%v requests=%d", err, len(bodies))
				}
				body := bodies[0]
				got := ""
				if filters, ok := body["Filter"].([]any); ok {
					for _, raw := range filters {
						f := raw.(map[string]any)
						if f["Key"] == "Region" {
							got = f["Values"].([]any)[0].(string)
						}
					}
				}
				if got != tc.want {
					t.Errorf("Region filter=%q, want %q", got, tc.want)
				}
				if body["NextToken"] != "prior-page" || body["MaxResults"] != float64(7) || page.NextToken != "next-page" {
					t.Errorf("pagination not preserved: request=%v page=%+v", body, page)
				}
			})
		}
	}
}
