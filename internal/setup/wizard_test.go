package setup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	_ "mcpcloud/internal/providers"
)

func wizardInput(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestWizardBuildsExplicitProfilesForEveryCloud(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	for _, tc := range []struct {
		cloud  model.Provider
		lines  []string
		scope  string
		region string
	}{
		{model.ProviderAWS, []string{"aws", "", "default", "", "123456789012", "us-east-1", "", "", "y"}, "123456789012", "us-east-1"},
		{model.ProviderGCP, []string{"gcp", "", "default", "projects", "project-a", "us-central1", "", "", "y"}, "project-a", "us-central1"},
		{model.ProviderAzure, []string{"azure", "", "default", "subscription-a", "eastus", "", "", "y"}, "subscription-a", "eastus"},
		{model.ProviderAlibaba, []string{"alibaba", "", "default", "12345", "cn-hangzhou", "", "", "y"}, "12345", "cn-hangzhou"},
		{model.ProviderHuawei, []string{"huawei", "", "env", "n", "project-a", "cn-north-4", "", "", "y"}, "project-a", "cn-north-4"},
		{model.ProviderTencent, []string{"tencent", "", "default", "12345", "ap-guangzhou", "", "", "y"}, "12345", "ap-guangzhou"},
		{model.ProviderVolcengine, []string{"volcengine", "", "default", "12345", "cn-beijing", "", "", "y"}, "12345", "cn-beijing"},
	} {
		t.Run(string(tc.cloud), func(t *testing.T) {
			var output bytes.Buffer
			wizard := Wizard{Input: strings.NewReader(wizardInput(tc.lines...)), Output: &output, Offline: true, Discover: func(context.Context, config.Profile) (Discovery, error) {
				t.Fatal("offline discovery called")
				return Discovery{}, nil
			}}
			name, p, err := wizard.Run(context.Background(), config.Default())
			if err != nil {
				t.Fatal(err)
			}
			if name != string(tc.cloud)+"-main" || p.Provider != tc.cloud || !reflect.DeepEqual(p.Regions, []string{tc.region}) {
				t.Fatalf("profile = %#v", p)
			}
			scopes := append(append(append([]string{}, p.Scopes.Accounts...), p.Scopes.Projects...), p.Scopes.Subscriptions...)
			if !reflect.DeepEqual(scopes, []string{tc.scope}) {
				t.Fatalf("scope = %v", scopes)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := SaveProfile(path, name, p); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Profiles[name], p) {
				t.Fatalf("saved profile changed: %#v", cfg.Profiles[name])
			}
		})
	}
}

func TestWizardRequiresSelectionAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, scopes, regions, answer string
		wantError                     bool
	}{
		{"one discovered project", "#2", "us-central1", "y", false},
		{"cancel", "#1", "us-central1", "n", true},
		{"default is cancel", "#1", "us-central1", "", true},
		{"unknown candidate", "#3", "us-central1", "y", true},
		{"wildcard scope", "*", "us-central1", "y", true},
		{"wildcard region", "#1", "*", "y", true},
		{"no region", "#1", "", "y", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wizard := Wizard{Input: strings.NewReader(wizardInput("gcp", "", "default", "projects", tc.scopes, tc.regions, "", "", tc.answer)), Output: &bytes.Buffer{}, Discover: func(context.Context, config.Profile) (Discovery, error) {
				return Discovery{Scopes: []string{"project-a", "project-b"}, Incomplete: true}, nil
			}}
			_, p, err := wizard.Run(context.Background(), config.Default())
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v", err)
			}
			if !tc.wantError && !reflect.DeepEqual(p.Scopes.Projects, []string{"project-b"}) {
				t.Fatalf("discovery expanded scope: %#v", p)
			}
		})
	}
}

func TestWizardCredentialReferencesDoNotCopySecrets(t *testing.T) {
	t.Setenv("TEST_SETUP_AK", "example-secret-access")
	t.Setenv("TEST_SETUP_SK", "example-secret-key")
	t.Setenv("TEST_SETUP_TOKEN", "example-secret-token")
	var output bytes.Buffer
	wizard := Wizard{Offline: true, Output: &output, Input: strings.NewReader(wizardInput("tencent", "custom", "env", "y", "TEST_SETUP_AK", "TEST_SETUP_SK", "TEST_SETUP_TOKEN", "12345", "ap-guangzhou", "", "", "y"))}
	_, p, err := wizard.Run(context.Background(), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if p.Credential.Env["TENCENTCLOUD_SECRET_KEY"] != "TEST_SETUP_SK" {
		t.Fatalf("references = %v", p.Credential.Env)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveProfile(path, "custom", p); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{output.String(), string(content)} {
		if strings.Contains(text, "example-secret") {
			t.Fatal("credential values escaped into output")
		}
	}
}

func TestWizardCostSetupAndFolderResources(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lines   []string
		options map[string]string
		fail    bool
	}{
		{"gcp costs", []string{"gcp", "", "default", "projects", "project-a", "us-central1", "", "costs", "billing.export.daily", "", "US", "y"}, map[string]string{"billing_table": "billing.export.daily", "billing_location": "US"}, false},
		{"folder resources", []string{"gcp", "", "default", "folders", "100", "us-central1", "", "resources", "y"}, map[string]string{"asset_scope": "folders/100"}, false},
		{"folder costs cannot widen billing", []string{"gcp", "", "default", "folders", "100", "us-central1", "", "costs"}, nil, true},
		{"tencent costs", []string{"tencent", "", "default", "12345", "ap-guangzhou", "", "costs", "billing", "CNY", "y"}, map[string]string{"billing_source": "billing", "billing_currency": "CNY"}, false},
		{"huawei costs", []string{"huawei", "", "env", "n", "project-a", "cn-north-4", "", "costs", "account-a", "china", "y"}, map[string]string{"billing_site": "china"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wizard := Wizard{Offline: true, Output: &bytes.Buffer{}, Input: strings.NewReader(wizardInput(tc.lines...))}
			_, p, err := wizard.Run(context.Background(), config.Default())
			if (err != nil) != tc.fail {
				t.Fatalf("error = %v", err)
			}
			if !tc.fail && !reflect.DeepEqual(p.Options, tc.options) {
				t.Fatalf("options = %v", p.Options)
			}
		})
	}
}

func TestWizardDiscoveryFailureAllowsManualScope(t *testing.T) {
	var output bytes.Buffer
	wizard := Wizard{Output: &output, Input: strings.NewReader(wizardInput("gcp", "", "default", "projects", "project-a", "us-central1", "", "", "y")), Discover: func(context.Context, config.Profile) (Discovery, error) {
		return Discovery{}, errors.New("private-secret-provider-response")
	}}
	_, p, err := wizard.Run(context.Background(), config.Default())
	if err != nil || len(p.Scopes.Projects) != 1 {
		t.Fatalf("profile = %#v, error = %v", p, err)
	}
	if strings.Contains(output.String(), "private-secret") {
		t.Fatal("provider error exposed")
	}
}

func TestAWSProfilesReadsOnlyNamedSections(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config")
	credentialsPath := filepath.Join(dir, "credentials")
	if err := os.WriteFile(configPath, []byte("[profile team]\nregion=us-east-1\n[sso-session login]\nsso_start_url=https://example.test\n[default]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialsPath, []byte("[team]\naws_secret_access_key=must-not-appear\n[deployment]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", configPath)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credentialsPath)
	if got := AWSProfiles(); !reflect.DeepEqual(got, []string{"default", "deployment", "team"}) {
		t.Fatalf("profiles = %v", got)
	}
}

func TestWizardSelectedAWSProfileIsUsedForDiscovery(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	calls := 0
	wizard := Wizard{Input: strings.NewReader(wizardInput("aws", "", "profile", "", "read-only-team", "", "#1", "us-east-1", "", "", "y")), Output: &bytes.Buffer{}, Discover: func(_ context.Context, p config.Profile) (Discovery, error) {
		calls++
		if p.Credential.Source != "profile" || p.Credential.Profile != "read-only-team" {
			t.Fatalf("selected identity lost: %#v", p.Credential)
		}
		return Discovery{Scopes: []string{"123456789012"}}, nil
	}}
	_, p, err := wizard.Run(context.Background(), config.Default())
	if err != nil || calls != 1 || p.Credential.Profile != "read-only-team" {
		t.Fatalf("profile = %#v, calls = %d, err = %v", p, calls, err)
	}
}
