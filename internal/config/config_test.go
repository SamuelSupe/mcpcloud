package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcpcloud/internal/model"
)

func validProfile() Profile {
	return Profile{
		Provider: model.ProviderAWS,
		Credential: Credential{
			Source: "env",
			Env: map[string]string{
				"AWS_ACCESS_KEY_ID":     "AWS_ACCESS_KEY_ID",
				"AWS_SECRET_ACCESS_KEY": "AWS_SECRET_ACCESS_KEY",
			},
		},
	}
}

func validProfileForProvider(provider model.Provider) Profile {
	profile := validProfile()
	profile.Provider = provider
	profile.Credential = Credential{Source: "default"}
	if provider == model.ProviderHuawei {
		profile.Credential = Credential{
			Source: "env",
			Env:    map[string]string{"HUAWEICLOUD_SDK_AK": "HUAWEICLOUD_SDK_AK"},
		}
	}
	return profile
}

func TestConfigValidateRejectsCredentialSecrets(t *testing.T) {
	cfg := Default()
	cfg.Profiles = map[string]Profile{
		"production": {
			Provider: model.ProviderAWS,
			Credential: Credential{
				Source: "env",
				Env: map[string]string{
					"AWS_SECRET_ACCESS_KEY": "actual-secret-value",
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() accepted a literal credential in credential.env")
	}
	if !strings.Contains(err.Error(), "environment variable name") {
		t.Fatalf("Validate() error = %q, want an environment-variable-name rejection", err)
	}
}

func TestConfigValidateAcceptsEnvironmentVariableCredentialNames(t *testing.T) {
	cfg := Default()
	cfg.Profiles = map[string]Profile{"production": validProfile()}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected environment variable names: %v", err)
	}
}

func TestConfigValidateCredentialEnvProviderAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider model.Provider
		key      string
	}{
		{name: "unknown AWS key", provider: model.ProviderAWS, key: "AWS_REGION"},
		{name: "GCP key on AWS", provider: model.ProviderAWS, key: "GOOGLE_APPLICATION_CREDENTIALS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			profile := validProfileForProvider(tc.provider)
			profile.Credential.Source = "env"
			profile.Credential.Env = map[string]string{tc.key: "MCP_CLOUD_TEST_ENV"}
			cfg.Profiles = map[string]Profile{"production": profile}

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "credential.env key") || !strings.Contains(err.Error(), "not supported") {
				t.Fatalf("Validate() error = %v, want provider credential.env allowlist rejection", err)
			}
		})
	}

	cfg := Default()
	profile := validProfile()
	profile.Credential.Env["AWS_SESSION_TOKEN"] = "AWS_SESSION_TOKEN"
	cfg.Profiles = map[string]Profile{"production": profile}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected optional AWS session-token env key: %v", err)
	}

	cfg = Default()
	huawei := validProfileForProvider(model.ProviderHuawei)
	huawei.Credential.Env["HUAWEICLOUD_DOMAIN_ID"] = "HUAWEICLOUD_DOMAIN_ID"
	cfg.Profiles = map[string]Profile{"huawei": huawei}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected optional Huawei domain-id env key: %v", err)
	}
}

func TestConfigRejectsRemovedFileCredentialSourceAndField(t *testing.T) {
	t.Run("Validate rejects source=file", func(t *testing.T) {
		cfg := Default()
		profile := validProfile()
		profile.Credential.Source = "file"
		profile.Credential.Env = nil
		cfg.Profiles = map[string]Profile{"production": profile}

		want := `profile "production" credential.source must be default, profile, env, or workload`
		if err := cfg.Validate(); err == nil || err.Error() != want {
			t.Fatalf("Validate() error = %v, want %q", err, want)
		}
	})

	t.Run("Load rejects credential.file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		data := []byte("profiles:\n" +
			"  production:\n" +
			"    provider: aws\n" +
			"    credential:\n" +
			"      source: env\n" +
			"      file: /tmp/credentials\n")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), "field file not found in type config.Credential") {
			t.Fatalf("Load() error = %v, want KnownFields rejection for credential.file", err)
		}
	})
}

func TestConfigValidateProviderOptionMatrix(t *testing.T) {
	allowed := map[model.Provider]map[string]string{
		model.ProviderAWS:        {},
		model.ProviderGCP:        {"asset_scope": "projects/example", "billing_table": "project.dataset.table", "billing_project": "project", "billing_location": "US"},
		model.ProviderAzure:      {},
		model.ProviderAlibaba:    {"resource_view": "view-1"},
		model.ProviderHuawei:     {"billing_site": "china"},
		model.ProviderTencent:    {"resource_view_id": "view-1", "billing_currency": "USD"},
		model.ProviderVolcengine: {},
	}
	allOptions := map[string]string{
		"asset_scope":      "projects/example",
		"billing_table":    "project.dataset.table",
		"billing_project":  "project",
		"billing_location": "US",
		"resource_view":    "view-1",
		"billing_site":     "china",
		"resource_view_id": "view-1",
		"billing_currency": "USD",
	}

	for _, provider := range model.Providers {
		provider := provider
		t.Run(string(provider)+"/allowed-options", func(t *testing.T) {
			cfg := Default()
			profile := validProfileForProvider(provider)
			profile.Options = allowed[provider]
			cfg.Profiles = map[string]Profile{"production": profile}

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() rejected allowed options for %s: %v", provider, err)
			}
		})

		for option, value := range allOptions {
			option, value := option, value
			if _, ok := allowed[provider][option]; ok {
				continue
			}
			t.Run(string(provider)+"/rejects-"+option, func(t *testing.T) {
				cfg := Default()
				profile := validProfileForProvider(provider)
				profile.Options = map[string]string{option: value}
				cfg.Profiles = map[string]Profile{"production": profile}

				err := cfg.Validate()
				if err == nil || !strings.Contains(err.Error(), "is not allowed") {
					t.Fatalf("Validate() error = %v, want provider option allowlist rejection", err)
				}
			})
		}
	}
}

func TestConfigValidateProviderOptionValueGuards(t *testing.T) {
	tests := []struct {
		name      string
		provider  model.Provider
		option    string
		value     string
		wantError string
	}{
		{name: "empty option", provider: model.ProviderAlibaba, option: "resource_view", value: "", wantError: "non-empty single-line identifier"},
		{name: "whitespace option", provider: model.ProviderGCP, option: "asset_scope", value: "  ", wantError: "non-empty single-line identifier"},
		{name: "newline option", provider: model.ProviderTencent, option: "resource_view_id", value: "view\n1", wantError: "non-empty single-line identifier"},
		{name: "nul option", provider: model.ProviderGCP, option: "billing_table", value: "project\x00.dataset.table", wantError: "non-empty single-line identifier"},
		{name: "invalid Huawei billing site", provider: model.ProviderHuawei, option: "billing_site", value: "global", wantError: "must be china or intl"},
		{name: "lowercase Tencent currency", provider: model.ProviderTencent, option: "billing_currency", value: "usd", wantError: "three-letter uppercase currency code"},
		{name: "short Tencent currency", provider: model.ProviderTencent, option: "billing_currency", value: "US", wantError: "three-letter uppercase currency code"},
		{name: "punctuated Tencent currency", provider: model.ProviderTencent, option: "billing_currency", value: "US$", wantError: "three-letter uppercase currency code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			profile := validProfileForProvider(tt.provider)
			profile.Options = map[string]string{tt.option: tt.value}
			cfg.Profiles = map[string]Profile{"production": profile}

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want an error containing %q", err, tt.wantError)
			}
		})
	}

	for _, site := range []string{"china", "intl"} {
		cfg := Default()
		profile := validProfileForProvider(model.ProviderHuawei)
		profile.Options = map[string]string{"billing_site": site}
		cfg.Profiles = map[string]Profile{"production": profile}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected Huawei billing_site=%q: %v", site, err)
		}
	}
}

func TestConfigValidateProfilesAndProviders(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{
			name: "blank profile name",
			configure: func(cfg *Config) {
				cfg.Profiles = map[string]Profile{" \t": validProfile()}
			},
			wantError: "profile name must be non-empty, bounded, and single-line",
		},
		{
			name: "unsupported provider",
			configure: func(cfg *Config) {
				profile := validProfile()
				profile.Provider = model.Provider("unsupported")
				cfg.Profiles = map[string]Profile{"production": profile}
			},
			wantError: "unsupported provider",
		},
		{
			name: "unsupported credential source",
			configure: func(cfg *Config) {
				profile := validProfile()
				profile.Credential.Source = "literal"
				cfg.Profiles = map[string]Profile{"production": profile}
			},
			wantError: "credential.source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.configure(&cfg)

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want an error containing %q", err, tt.wantError)
			}
		})
	}

	for _, provider := range model.Providers {
		cfg := Default()
		profile := validProfileForProvider(provider)
		cfg.Profiles = map[string]Profile{"production": profile}

		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate() rejected supported provider %q: %v", provider, err)
		}
	}
}

func TestConfigValidateLoopbackListenAddresses(t *testing.T) {
	tests := []struct {
		name      string
		listen    string
		wantError string
	}{
		{name: "ipv4 loopback", listen: "127.0.0.1:8080"},
		{name: "ipv6 loopback", listen: "[::1]:8080"},
		{name: "localhost", listen: "localhost:8080"},
		{name: "empty listen", listen: "", wantError: "server.listen is required"},
		{name: "non-loopback address", listen: "192.0.2.10:8080", wantError: "loopback address"},
		{name: "all interfaces", listen: "0.0.0.0:8080", wantError: "loopback address"},
		{name: "missing port", listen: "127.0.0.1", wantError: "server.listen:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			cfg.Server.Listen = tt.listen

			err := cfg.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() rejected %q: %v", tt.listen, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want an error containing %q", err, tt.wantError)
			}
		})
	}
}

func TestConfigValidateLimitsBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*Config)
		wantError string
	}{
		{name: "max targets lower bound", configure: func(cfg *Config) { cfg.Limits.MaxTargets = 1 }},
		{name: "max targets upper bound", configure: func(cfg *Config) { cfg.Limits.MaxTargets = 128 }},
		{name: "max targets below lower bound", configure: func(cfg *Config) { cfg.Limits.MaxTargets = 0 }, wantError: "limits.max_targets"},
		{name: "max targets above upper bound", configure: func(cfg *Config) { cfg.Limits.MaxTargets = 129 }, wantError: "limits.max_targets"},
		{name: "provider requests lower bound", configure: func(cfg *Config) { cfg.Limits.MaxProviderRequests = 1 }},
		{name: "provider requests upper bound", configure: func(cfg *Config) { cfg.Limits.MaxProviderRequests = 500 }},
		{name: "provider requests below lower bound", configure: func(cfg *Config) { cfg.Limits.MaxProviderRequests = 0 }, wantError: "limits.max_provider_requests"},
		{name: "provider requests above upper bound", configure: func(cfg *Config) { cfg.Limits.MaxProviderRequests = 501 }, wantError: "limits.max_provider_requests"},
		{name: "scanned items lower bound", configure: func(cfg *Config) { cfg.Limits.MaxScannedItems = 1 }},
		{name: "scanned items upper bound", configure: func(cfg *Config) { cfg.Limits.MaxScannedItems = 50_000 }},
		{name: "scanned items below lower bound", configure: func(cfg *Config) { cfg.Limits.MaxScannedItems = 0 }, wantError: "limits.max_scanned_items"},
		{name: "scanned items above upper bound", configure: func(cfg *Config) { cfg.Limits.MaxScannedItems = 50_001 }, wantError: "limits.max_scanned_items"},
		{name: "page size lower bound", configure: func(cfg *Config) { cfg.Limits.MaxPageSize = 1 }},
		{name: "page size upper bound", configure: func(cfg *Config) { cfg.Limits.MaxPageSize = 500 }},
		{name: "page size below lower bound", configure: func(cfg *Config) { cfg.Limits.MaxPageSize = 0 }, wantError: "limits.max_page_size"},
		{name: "page size above upper bound", configure: func(cfg *Config) { cfg.Limits.MaxPageSize = 501 }, wantError: "limits.max_page_size"},
		{name: "default timeout lower bound", configure: func(cfg *Config) { cfg.Limits.DefaultTimeout = time.Nanosecond }},
		{name: "default timeout above max timeout", configure: func(cfg *Config) {
			cfg.Limits.DefaultTimeout = 31 * time.Second
			cfg.Limits.MaxTimeout = 30 * time.Second
		}, wantError: "timeout limits"},
		{name: "default timeout zero", configure: func(cfg *Config) { cfg.Limits.DefaultTimeout = 0 }, wantError: "timeout limits"},
		{name: "max timeout upper bound", configure: func(cfg *Config) { cfg.Limits.MaxTimeout = 120 * time.Second }},
		{name: "max timeout below lower bound", configure: func(cfg *Config) { cfg.Limits.MaxTimeout = 0 }, wantError: "timeout limits"},
		{name: "max timeout above upper bound", configure: func(cfg *Config) { cfg.Limits.MaxTimeout = 121 * time.Second }, wantError: "timeout limits"},
		{name: "default equals max timeout", configure: func(cfg *Config) {
			cfg.Limits.DefaultTimeout = 120 * time.Second
			cfg.Limits.MaxTimeout = 120 * time.Second
		}},
		{name: "cursor ttl lower bound", configure: func(cfg *Config) { cfg.Limits.CursorTTL = time.Nanosecond }},
		{name: "cursor ttl upper bound", configure: func(cfg *Config) { cfg.Limits.CursorTTL = 10 * time.Minute }},
		{name: "cursor ttl below lower bound", configure: func(cfg *Config) { cfg.Limits.CursorTTL = 0 }, wantError: "limits.cursor_ttl"},
		{name: "cursor ttl above upper bound", configure: func(cfg *Config) { cfg.Limits.CursorTTL = 10*time.Minute + time.Nanosecond }, wantError: "limits.cursor_ttl"},
		{name: "concurrency lower bound", configure: func(cfg *Config) { cfg.Limits.Concurrency = 1 }},
		{name: "concurrency upper bound", configure: func(cfg *Config) { cfg.Limits.Concurrency = 32 }},
		{name: "concurrency below lower bound", configure: func(cfg *Config) { cfg.Limits.Concurrency = 0 }, wantError: "limits.concurrency"},
		{name: "concurrency above upper bound", configure: func(cfg *Config) { cfg.Limits.Concurrency = 33 }, wantError: "limits.concurrency"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.configure(&cfg)

			err := cfg.Validate()
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("Validate() rejected boundary value: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want an error containing %q", err, tt.wantError)
			}
		})
	}
}
