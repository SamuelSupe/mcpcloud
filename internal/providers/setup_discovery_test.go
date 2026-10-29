package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

func TestSetupIdentityDiscoveryUsesCurrentCredentialAndReadOnlyActions(t *testing.T) {
	for _, tc := range []struct {
		cloud      model.Provider
		host, body string
		env        map[string]string
	}{
		{model.ProviderAlibaba, "sts.cn-hangzhou.aliyuncs.com", `{"AccountId":"12345"}`, map[string]string{"ALIBABA_CLOUD_ACCESS_KEY_ID": "test-ak", "ALIBABA_CLOUD_ACCESS_KEY_SECRET": "test-sk", "ALIBABA_CLOUD_SECURITY_TOKEN": "test-session"}},
		{model.ProviderTencent, "sts.tencentcloudapi.com", `{"Response":{"AccountId":"12345","RequestId":"test"}}`, map[string]string{"TENCENTCLOUD_SECRET_ID": "test-id", "TENCENTCLOUD_SECRET_KEY": "test-key", "TENCENTCLOUD_TOKEN": "test-session"}},
		{model.ProviderVolcengine, "open.volcengineapi.com", `{"ResponseMetadata":{"RequestId":"test","Action":"GetCallerIdentity","Version":"2018-01-01","Service":"sts","Region":"cn-beijing"},"Result":{"AccountId":12345}}`, map[string]string{"VOLCENGINE_ACCESS_KEY_ID": "test-id", "VOLCENGINE_SECRET_ACCESS_KEY": "test-key", "VOLCENGINE_SESSION_TOKEN": "test-session"}},
	} {
		t.Run(string(tc.cloud), func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			calls := 0
			transport := providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != tc.host {
					t.Fatalf("unexpected endpoint %s", r.URL.Host)
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("discovery request has no deadline")
				}
				action := r.Header.Get("X-TC-Action")
				if action == "" {
					action = r.URL.Query().Get("Action")
				}
				if action == "" {
					r.ParseForm()
					action = r.Form.Get("Action")
				}
				if action != "GetCallerIdentity" {
					t.Fatalf("unexpected action: %q", action)
				}
				return providerTestHTTPResponse(r, tc.body), nil
			})
			if tc.cloud == model.ProviderVolcengine {
				originalClient := http.DefaultClient
				t.Cleanup(func() { http.DefaultClient = originalClient })
				http.DefaultClient = &http.Client{Transport: transport}
			} else {
				http.DefaultTransport = transport
			}
			result, err := DiscoverSetup(context.Background(), config.Profile{Provider: tc.cloud, Credential: config.Credential{Source: "env"}})
			if err != nil || !reflect.DeepEqual(result.Scopes, []string{"12345"}) || calls != 1 {
				t.Fatalf("result = %#v, err = %v, calls = %d", result, err, calls)
			}
		})
	}
}

func TestSetupAWSDiscoveryUsesSelectedSharedProfileRegion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "/us-west-2/sts/") {
			t.Errorf("selected profile region was not used")
		}
		r.ParseForm()
		if r.Form.Get("Action") != "GetCallerIdentity" {
			t.Errorf("unexpected action %q", r.Form.Get("Action"))
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>test</UserId><Account>123456789012</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>test</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "aws-config")
	if err := os.WriteFile(path, []byte("[profile setup-test]\nregion=us-west-2\naws_access_key_id=test-id\naws_secret_access_key=test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "absent"))
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	result, err := DiscoverSetup(context.Background(), config.Profile{Provider: model.ProviderAWS, Credential: config.Credential{Source: "profile", Profile: "setup-test"}})
	if err != nil || result.Region != "us-west-2" || !reflect.DeepEqual(result.Scopes, []string{"123456789012"}) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestSetupGCPProjectDiscoveryIsBoundedAndFiltersDeletedProjects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte(`{"type":"authorized_user","client_id":"test-client","client_secret":"test-secret","refresh_token":"test-refresh"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SETUP_GCP_CREDENTIAL_FILE", path)
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	searches := 0
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "oauth2.googleapis.com" {
			return providerTestHTTPResponse(r, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`), nil
		}
		if r.URL.Host != "cloudresourcemanager.googleapis.com" || r.URL.Path != "/v3/projects:search" || r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("unexpected search URL: %s", r.URL)
		}
		searches++
		return providerTestHTTPResponse(r, `{"projects":[{"projectId":"project-a","state":"ACTIVE"},{"projectId":"deleted","state":"DELETE_REQUESTED"},{"projectId":"bad\u001bvalue","state":"ACTIVE"},{"projectId":"project-a","state":"ACTIVE"}],"nextPageToken":"page-two"}`), nil
	})
	result, err := DiscoverSetup(context.Background(), config.Profile{Provider: model.ProviderGCP, Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "SETUP_GCP_CREDENTIAL_FILE"}}})
	if err != nil || searches != 1 || !result.Incomplete || !reflect.DeepEqual(result.Scopes, []string{"project-a"}) {
		t.Fatalf("result = %#v, err = %v, searches = %d", result, err, searches)
	}
}

func TestSetupAzureSubscriptionDiscoveryDoesNotFollowNextLink(t *testing.T) {
	authority := httptest.NewTLSServer(nil)
	defer authority.Close()
	useAzureTestAuthority(t, authority)
	t.Setenv("AZURE_AUTHORITY_HOST", authority.URL)
	t.Setenv("AZURE_TENANT_ID", "adfs")
	t.Setenv("AZURE_CLIENT_ID", "test-client")
	t.Setenv("AZURE_CLIENT_SECRET", "test-secret")
	authority.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, ".well-known/openid-configuration") {
			fmt.Fprintf(w, `{"authorization_endpoint":%q,"issuer":%q,"token_endpoint":%q}`, authority.URL+"/adfs/v2.0", authority.URL+"/adfs/v2.0", authority.URL+"/adfs/oauth2/v2.0/token")
			return
		}
		io.WriteString(w, `{"access_token":"azure-test-token","token_type":"Bearer","expires_in":3600}`)
	})
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	calls := 0
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == strings.TrimPrefix(authority.URL, "https://") {
			return authority.Client().Transport.RoundTrip(r)
		}
		if r.URL.Host != "management.azure.com" || r.URL.Path != "/subscriptions" || r.URL.Query().Get("api-version") != "2022-12-01" {
			t.Fatalf("unexpected Azure URL: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer azure-test-token" {
			t.Fatal("missing expected credential")
		}
		calls++
		return providerTestHTTPResponse(r, `{"value":[{"subscriptionId":"sub-a","state":"Enabled"},{"subscriptionId":"sub-b","state":"Disabled"}],"nextLink":"https://untrusted.example/next"}`), nil
	})
	result, err := DiscoverSetup(context.Background(), config.Profile{Provider: model.ProviderAzure, Credential: config.Credential{Source: "env"}})
	if err != nil || calls != 1 || !result.Incomplete || !reflect.DeepEqual(result.Scopes, []string{"sub-a"}) {
		t.Fatalf("result = %#v, err = %v, calls = %d", result, err, calls)
	}
}

func TestSetupDiscoveryHonorsCancellationAndHidesProviderResponse(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "test-id")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "test-key")
	previous := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previous })
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := DiscoverSetup(ctx, config.Profile{Provider: model.ProviderAlibaba, Credential: config.Credential{Source: "env"}})
	if err == nil || ctx.Err() == nil {
		t.Fatalf("cancellation ignored: %v", err)
	}
	http.DefaultTransport = providerTestRoundTripper(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("private-secret-provider-response")
	})
	_, err = DiscoverSetup(context.Background(), config.Profile{Provider: model.ProviderAlibaba, Credential: config.Credential{Source: "env"}})
	if err == nil || strings.Contains(err.Error(), "private-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSetupGetRejectsRedirectsAndOversizedResponses(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	for _, kind := range []string{"redirect", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if kind == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusFound)
					return
				}
				io.WriteString(w, strings.Repeat(" ", (1<<20)+1))
			}))
			defer server.Close()
			var result any
			if err := setupGet(context.Background(), server.Client(), server.URL, "private-token", &result); err == nil {
				t.Fatal("invalid response accepted")
			}
		})
	}
	if targetCalls != 0 {
		t.Fatal("discovery followed a redirect")
	}
}
