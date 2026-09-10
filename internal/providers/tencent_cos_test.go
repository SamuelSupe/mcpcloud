package providers

import (
	"context"
	"encoding/json"
	"io"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
	"net/http"
	"strings"
	"testing"
)

func TestTencentCOSReadBoundariesAndConfigurations(t *testing.T) {
	t.Setenv("TENCENTCLOUD_SECRET_ID", "test-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "test-key")
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	a := &tencentAdapter{name: "test", profile: config.Profile{Credential: config.Credential{Source: "env"}, Regions: []string{"ap-jakarta"}, Scopes: config.Scopes{Accounts: []string{"123"}}}}
	for _, tc := range []struct {
		name, owner, location, version, life string
		status                               int
		wantErr                              bool
		wantCalls                            int
	}{
		{"absent", "123", "ap-jakarta", "", "", 404, false, 4},
		{"configured", "123", "ap-jakarta", "Enabled", `<LifecycleConfiguration><Rule><ID>secret-rule</ID><Status>Enabled</Status><Filter><Prefix>secret-prefix</Prefix></Filter></Rule></LifecycleConfiguration>`, 200, false, 4},
		{"denied", "123", "ap-jakarta", "Suspended", `<Error><Code>AccessDenied</Code><Message>secret-message</Message></Error>`, 403, true, 4},
		{"wrong-owner", "456", "ap-jakarta", "Enabled", "", 404, true, 1},
		{"wrong-region", "123", "ap-shanghai", "Enabled", "", 404, true, 2},
		{"bad-version", "123", "ap-jakarta", "unexpected", "", 404, true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			http.DefaultTransport = providerTestRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Host != "test-bucket-12345.cos.ap-jakarta.myqcloud.com" || req.URL.Path != "/" {
					t.Fatal("unexpected endpoint")
				}
				if req.Header.Get("Authorization") == "" {
					t.Fatal("unsigned request")
				}
				status := 200
				body := ""
				switch req.URL.RawQuery {
				case "acl":
					body = `<AccessControlPolicy><Owner><ID>qcs::cam::uin/` + tc.owner + `:uin/` + tc.owner + `</ID></Owner><AccessControlList><Grant><Grantee><URI>http://cam.qcloud.com/groups/global/AllUsers</URI></Grantee><Permission>READ</Permission></Grant></AccessControlList></AccessControlPolicy>`
				case "location":
					body = `<LocationConstraint>` + tc.location + `</LocationConstraint>`
				case "versioning":
					body = `<VersioningConfiguration><Status>` + tc.version + `</Status></VersioningConfiguration>`
				case "lifecycle":
					status = tc.status
					body = tc.life
					if body == "" {
						body = `<Error><Code>NoSuchLifecycleConfiguration</Code></Error>`
					}
				default:
					t.Fatal("unexpected query")
				}
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			p, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: tencentCOSOperation, Region: "ap-jakarta", Params: map[string]any{"bucket": "test-bucket-12345"}})
			if (err != nil) != tc.wantErr || calls != tc.wantCalls {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if err != nil {
				if strings.Contains(err.Error(), "secret-") {
					t.Fatal("error leaked")
				}
				return
			}
			raw, _ := json.Marshal(p.Rows)
			if strings.Contains(string(raw), "secret-") {
				t.Fatal("configuration leaked")
			}
			attrs := p.Rows[0]["attributes"].(map[string]any)
			if attrs["acl_all_users_grant"] != true {
				t.Fatal("public grant missed")
			}
			if tc.name == "absent" && (attrs["versioning_status"] != "not_enabled" || attrs["lifecycle_configured"] != false) {
				t.Fatal("absent configuration mapping")
			}
			if tc.name == "configured" && attrs["lifecycle_rule_count"] != 1 {
				t.Fatal("rule count")
			}
		})
	}
	http.DefaultTransport = providerTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached network")
		return nil, nil
	})
	for _, bucket := range []string{"", "https://evil.test", "x/../test-12345", "bucket-12345.evil.test"} {
		if _, err := a.NativeRead(context.Background(), provider.NativeRequest{Operation: tencentCOSOperation, Region: "ap-jakarta", Params: map[string]any{"bucket": bucket}}); err == nil {
			t.Fatal("invalid bucket accepted")
		}
	}
}
