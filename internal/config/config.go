package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"mcpcloud/internal/model"
)

var regionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Config struct {
	Server   ServerConfig       `yaml:"server" json:"server"`
	Limits   Limits             `yaml:"limits" json:"limits"`
	Profiles map[string]Profile `yaml:"profiles" json:"profiles"`
}

type ServerConfig struct {
	Listen         string   `yaml:"listen" json:"listen"`
	AuthTokenEnv   string   `yaml:"auth_token_env" json:"auth_token_env"`
	AllowedHosts   []string `yaml:"allowed_hosts" json:"allowed_hosts"`
	AllowedOrigins []string `yaml:"allowed_origins" json:"allowed_origins"`
}

type Limits struct {
	MaxTargets          int           `yaml:"max_targets" json:"max_targets"`
	MaxProviderRequests int           `yaml:"max_provider_requests" json:"max_provider_requests"`
	MaxScannedItems     int           `yaml:"max_scanned_items" json:"max_scanned_items"`
	MaxPageSize         int           `yaml:"max_page_size" json:"max_page_size"`
	DefaultTimeout      time.Duration `yaml:"default_timeout" json:"default_timeout"`
	MaxTimeout          time.Duration `yaml:"max_timeout" json:"max_timeout"`
	CursorTTL           time.Duration `yaml:"cursor_ttl" json:"cursor_ttl"`
	Concurrency         int           `yaml:"concurrency" json:"concurrency"`
}

type Profile struct {
	Provider   model.Provider    `yaml:"provider" json:"provider"`
	Enabled    *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Credential Credential        `yaml:"credential" json:"credential"`
	Scopes     Scopes            `yaml:"scopes" json:"scopes"`
	Regions    []string          `yaml:"regions" json:"regions"`
	Services   []string          `yaml:"services" json:"services"`
	Options    map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

func (p Profile) IsEnabled() bool { return p.Enabled == nil || *p.Enabled }

type Credential struct {
	Source  string            `yaml:"source" json:"source"`
	Profile string            `yaml:"profile,omitempty" json:"profile,omitempty"`
	RoleARN string            `yaml:"role_arn,omitempty" json:"role_arn,omitempty"`
	Env     map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}

type Scopes struct {
	Organizations []string `yaml:"organizations,omitempty" json:"organizations,omitempty"`
	Tenants       []string `yaml:"tenants,omitempty" json:"tenants,omitempty"`
	Accounts      []string `yaml:"accounts,omitempty" json:"accounts,omitempty"`
	Projects      []string `yaml:"projects,omitempty" json:"projects,omitempty"`
	Subscriptions []string `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
}

func Default() Config {
	return Config{
		Server: ServerConfig{Listen: "127.0.0.1:8080", AuthTokenEnv: "MCPCLOUD_AUTH_TOKEN"},
		Limits: Limits{
			MaxTargets: 128, MaxProviderRequests: 500, MaxScannedItems: 50_000,
			MaxPageSize: 500, DefaultTimeout: 30 * time.Second, MaxTimeout: 120 * time.Second,
			CursorTTL: 10 * time.Minute, Concurrency: 8,
		},
		Profiles: map[string]Profile{},
	}
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "mcpcloud", "config.yaml"), nil
}

func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("parse config: multiple YAML documents are not supported")
		}
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Limits.MaxTargets <= 0 || c.Limits.MaxTargets > 128 {
		return errors.New("limits.max_targets must be between 1 and 128")
	}
	if c.Limits.MaxProviderRequests <= 0 || c.Limits.MaxProviderRequests > 500 {
		return errors.New("limits.max_provider_requests must be between 1 and 500")
	}
	if c.Limits.MaxScannedItems <= 0 || c.Limits.MaxScannedItems > 50_000 {
		return errors.New("limits.max_scanned_items must be between 1 and 50000")
	}
	if c.Limits.MaxPageSize <= 0 || c.Limits.MaxPageSize > 500 {
		return errors.New("limits.max_page_size must be between 1 and 500")
	}
	if c.Limits.DefaultTimeout <= 0 || c.Limits.MaxTimeout <= 0 || c.Limits.DefaultTimeout > c.Limits.MaxTimeout || c.Limits.MaxTimeout > 120*time.Second {
		return errors.New("timeout limits are invalid or exceed 120s")
	}
	if c.Limits.CursorTTL <= 0 || c.Limits.CursorTTL > 10*time.Minute {
		return errors.New("limits.cursor_ttl must be between 1ns and 10m")
	}
	if c.Limits.Concurrency <= 0 || c.Limits.Concurrency > 32 {
		return errors.New("limits.concurrency must be between 1 and 32")
	}
	if c.Server.Listen == "" {
		return errors.New("server.listen is required")
	}
	if c.Server.AuthTokenEnv != "" && looksLikeSecret(c.Server.AuthTokenEnv) {
		return errors.New("server.auth_token_env must be an environment variable name, not a token")
	}
	if err := validateLoopback(c.Server.Listen); err != nil {
		return err
	}
	for _, host := range c.Server.AllowedHosts {
		if strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n\x00") {
			return errors.New("server.allowed_hosts must contain exact, non-empty Host values")
		}
	}
	for _, origin := range c.Server.AllowedOrigins {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("server.allowed_origins contains invalid origin %q", origin)
		}
	}
	validProviders := map[model.Provider]bool{}
	for _, p := range model.Providers {
		validProviders[p] = true
	}
	for name, p := range c.Profiles {
		if strings.TrimSpace(name) == "" || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") {
			return errors.New("profile name must be non-empty, bounded, and single-line")
		}
		if !validProviders[p.Provider] {
			return fmt.Errorf("profile %q has unsupported provider %q", name, p.Provider)
		}
		if err := validateCredential(name, p.Provider, p.Credential); err != nil {
			return err
		}
		if !credentialSourceAllowed(p.Provider, p.Credential.Source) {
			return fmt.Errorf("profile %q provider %s does not support credential source %q", name, p.Provider, p.Credential.Source)
		}
		if p.Credential.RoleARN != "" && p.Provider != model.ProviderAWS {
			return fmt.Errorf("profile %q credential.role_arn is only supported for AWS", name)
		}
		if err := validateOptions(name, p.Provider, p.Options); err != nil {
			return err
		}
		if err := validateStrings(name, "regions", p.Regions); err != nil {
			return err
		}
		for _, region := range p.Regions {
			if region != "*" && !regionPattern.MatchString(region) {
				return fmt.Errorf("profile %q regions contains invalid region identifier %q", name, region)
			}
		}
		if err := validateStrings(name, "services", p.Services); err != nil {
			return err
		}
		for _, scoped := range []struct {
			field  string
			values []string
		}{
			{"scopes.organizations", p.Scopes.Organizations},
			{"scopes.tenants", p.Scopes.Tenants},
			{"scopes.accounts", p.Scopes.Accounts},
			{"scopes.projects", p.Scopes.Projects},
			{"scopes.subscriptions", p.Scopes.Subscriptions},
		} {
			if err := validateStrings(name, scoped.field, scoped.values); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateOptions(name string, provider model.Provider, options map[string]string) error {
	allowed := map[model.Provider]map[string]bool{
		model.ProviderAWS:        {},
		model.ProviderGCP:        {"asset_scope": true, "billing_table": true, "billing_project": true, "billing_location": true},
		model.ProviderAzure:      {},
		model.ProviderAlibaba:    {"resource_view": true},
		model.ProviderHuawei:     {"billing_site": true},
		model.ProviderTencent:    {"resource_view_id": true, "billing_currency": true, "resource_mode": true},
		model.ProviderVolcengine: {},
	}
	if provider == model.ProviderTencent && options["resource_mode"] == "direct" && options["resource_view_id"] != "" {
		return fmt.Errorf("profile %q direct resources cannot use resource_view_id", name)
	}
	for key, value := range options {
		if provider == model.ProviderTencent && key == "resource_mode" && value != "direct" && value != "cloudrc" {
			return fmt.Errorf("profile %q resource_mode must be direct or cloudrc", name)
		}
		if !allowed[provider][key] {
			return fmt.Errorf("profile %q option %q is not allowed for provider %s", name, key, provider)
		}
		if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("profile %q option %q must be a non-empty single-line identifier", name, key)
		}
		if provider == model.ProviderHuawei && key == "billing_site" && value != "china" && value != "intl" {
			return fmt.Errorf("profile %q option billing_site must be china or intl", name)
		}
		if provider == model.ProviderTencent && key == "billing_currency" && !isCurrencyCode(value) {
			return fmt.Errorf("profile %q option billing_currency must be a three-letter uppercase currency code", name)
		}
	}
	return nil
}

func isCurrencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func validateStrings(name, field string, values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("profile %q %s must contain non-empty, bounded, single-line values", name, field)
		}
		if seen[value] {
			return fmt.Errorf("profile %q %s contains duplicate %q", name, field, value)
		}
		seen[value] = true
	}
	return nil
}

func credentialSourceAllowed(provider model.Provider, source string) bool {
	allowed := map[model.Provider]map[string]bool{
		model.ProviderAWS:        {"default": true, "profile": true, "env": true, "workload": true},
		model.ProviderGCP:        {"default": true, "env": true, "workload": true},
		model.ProviderAzure:      {"default": true, "env": true, "workload": true},
		model.ProviderAlibaba:    {"default": true, "env": true},
		model.ProviderHuawei:     {"env": true},
		model.ProviderTencent:    {"default": true, "env": true},
		model.ProviderVolcengine: {"default": true, "env": true},
	}
	return allowed[provider][source]
}

func validateLoopback(listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("server.listen must use a loopback address; terminate remote HTTPS at a reverse proxy")
	}
	return nil
}

func validateCredential(name string, provider model.Provider, c Credential) error {
	switch c.Source {
	case "default", "profile", "env", "workload":
	default:
		return fmt.Errorf("profile %q credential.source must be default, profile, env, or workload", name)
	}
	if c.Source == "profile" && strings.TrimSpace(c.Profile) == "" {
		return fmt.Errorf("profile %q credential.profile is required when source is profile", name)
	}
	if len(c.Profile) > 256 || strings.ContainsAny(c.Profile, "\r\n\x00") {
		return fmt.Errorf("profile %q credential.profile must be bounded and single-line", name)
	}
	if c.Profile != "" && c.Source != "profile" {
		return fmt.Errorf("profile %q credential.profile is only valid when source is profile", name)
	}
	if len(c.Env) > 0 && c.Source != "env" {
		return fmt.Errorf("profile %q credential.env is only valid when source is env", name)
	}
	if len(c.RoleARN) > 2048 || strings.ContainsAny(c.RoleARN, "\r\n\x00") {
		return fmt.Errorf("profile %q credential.role_arn must be bounded and single-line", name)
	}
	for key, value := range c.Env {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return fmt.Errorf("profile %q credential.env requires non-empty names", name)
		}
		if !credentialEnvKeys[provider][key] {
			return fmt.Errorf("profile %q credential.env key %q is not supported for provider %s", name, key, provider)
		}
		if looksLikeSecret(value) {
			return fmt.Errorf("profile %q credential.env value must be an environment variable name, not a credential", name)
		}
	}
	return nil
}

var credentialEnvKeys = map[model.Provider]map[string]bool{
	model.ProviderAWS: {
		"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
	},
	model.ProviderGCP: {
		"GOOGLE_APPLICATION_CREDENTIALS": true,
	},
	model.ProviderAzure: {
		"AZURE_TENANT_ID": true, "AZURE_CLIENT_ID": true, "AZURE_CLIENT_SECRET": true,
	},
	model.ProviderAlibaba: {
		"ALIBABA_CLOUD_ACCESS_KEY_ID": true, "ALIBABA_CLOUD_ACCESS_KEY_SECRET": true, "ALIBABA_CLOUD_SECURITY_TOKEN": true,
	},
	model.ProviderHuawei: {
		"HUAWEICLOUD_SDK_AK": true, "HUAWEICLOUD_SDK_SK": true, "HUAWEICLOUD_SDK_SECURITY_TOKEN": true, "HUAWEICLOUD_DOMAIN_ID": true,
	},
	model.ProviderTencent: {
		"TENCENTCLOUD_SECRET_ID": true, "TENCENTCLOUD_SECRET_KEY": true, "TENCENTCLOUD_TOKEN": true,
	},
	model.ProviderVolcengine: {
		"VOLCENGINE_ACCESS_KEY_ID": true, "VOLCENGINE_SECRET_ACCESS_KEY": true, "VOLCENGINE_SESSION_TOKEN": true,
	},
}

func looksLikeSecret(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) > 96 || strings.ContainsAny(v, " \t\n") {
		return true
	}
	for _, r := range v {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return true
		}
	}
	return v == "" || strings.Contains(strings.ToLower(v), "actual")
}

func (c Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name, p := range c.Profiles {
		if p.IsEnabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}
