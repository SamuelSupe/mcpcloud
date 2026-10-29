package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

func testRequest() ConfigurationRequest {
	return ConfigurationRequest{Name: "agent", Profile: testProfile(), Features: []string{"resources"}}
}

func TestConfigurationPreviewIsReadOnlyAndApplyPreservesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new-directory", "config.yaml")
	request := testRequest()
	plan, candidate, err := PlanConfiguration(path, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ID) != 64 || !filepath.IsAbs(plan.ConfigPath) || len(candidate.Profiles) != 1 {
		t.Fatalf("plan = %#v, candidate = %#v", plan, candidate)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("preview created output directory: %v", err)
	}
	repeated, _, err := PlanConfiguration(path, request)
	if err != nil || repeated.ID != plan.ID {
		t.Fatalf("unchanged plan not reproducible: %v", err)
	}
	if _, err := ApplyConfiguration(path, request, plan.ID); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	original = append([]byte("# Existing deployment\nserver:\n  listen: 127.0.0.1:9090\nlimits:\n  concurrency: 2\n"), original...)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	request.Name = "additional"
	request.Profile.Scopes.Projects = []string{"project-b"}
	plan, _, err = PlanConfiguration(path, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyConfiguration(path, request, plan.ID); err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(path)
	if !reflect.DeepEqual(before.Profiles["agent"], after.Profiles["agent"]) || before.Limits != after.Limits || !reflect.DeepEqual(before.Server, after.Server) || !bytes.Contains(content, []byte("# Existing deployment")) {
		t.Fatal("apply changed unrelated configuration")
	}
	if _, err := ApplyConfiguration(path, request, plan.ID); !errors.Is(err, ErrProfileExists) {
		t.Fatalf("repeat apply = %v", err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(content, current) {
		t.Fatal("repeat apply changed existing profile")
	}
}

func TestConfigurationApplyBindsPreviewToRequestDestinationAndExistingFile(t *testing.T) {
	for _, change := range []string{"scope", "region", "credential", "features", "destination", "existing configuration"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			request := testRequest()
			if err := SaveProfile(path, "existing", testProfile()); err != nil {
				t.Fatal(err)
			}
			plan, _, err := PlanConfiguration(path, request)
			if err != nil {
				t.Fatal(err)
			}
			destination := path
			switch change {
			case "scope":
				request.Profile.Scopes.Projects = []string{"another-project"}
			case "region":
				request.Profile.Regions = []string{"europe-west1"}
			case "credential":
				request.Profile.Credential.Source = "workload"
			case "features":
				request.Features = []string{"metrics"}
			case "destination":
				destination = filepath.Join(t.TempDir(), "other.yaml")
			case "existing configuration":
				content, _ := os.ReadFile(path)
				if err := os.WriteFile(path, append(content, []byte("# concurrent edit\n")...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			expected, _ := os.ReadFile(path)
			if _, err := ApplyConfiguration(destination, request, plan.ID); !errors.Is(err, ErrConfigurationChanged) {
				t.Fatalf("changed plan accepted: %v", err)
			}
			current, _ := os.ReadFile(path)
			if !bytes.Equal(expected, current) {
				t.Fatal("conflict changed file")
			}
			if destination != path {
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("changed destination was written")
				}
			}
		})
	}
}

func TestConfigurationChecksReviewedRevisionInsideWriterLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	request := testRequest()
	plan, _, err := PlanConfiguration(path, request)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a change after apply's plan check, before the locked write.
	if err := SaveProfile(path, "concurrent", testProfile()); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := saveProfile(path, request.Name, request.Profile, &plan.baseRevision); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatalf("stale revision accepted under lock: %v", err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(original, current) {
		t.Fatal("concurrent edit lost")
	}
}

func TestConfigurationRequestRejectsBroadScopesAndInvalidReferences(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ConfigurationRequest)
	}{
		{"no scope", func(r *ConfigurationRequest) { r.Profile.Scopes = config.Scopes{} }},
		{"wildcard scope", func(r *ConfigurationRequest) { r.Profile.Scopes.Projects = []string{"*"} }},
		{"no regions", func(r *ConfigurationRequest) { r.Profile.Regions = nil }},
		{"wildcard region", func(r *ConfigurationRequest) { r.Profile.Regions = []string{"*"} }},
		{"no services", func(r *ConfigurationRequest) { r.Profile.Services = nil }},
		{"wildcard services", func(r *ConfigurationRequest) { r.Profile.Services = []string{"*"} }},
		{"disabled", func(r *ConfigurationRequest) { disabled := false; r.Profile.Enabled = &disabled }},
		{"unknown feature", func(r *ConfigurationRequest) { r.Features = []string{"write"} }},
		{"unknown credential", func(r *ConfigurationRequest) { r.Profile.Credential.Source = "secret-must-not-appear" }},
		{"secret instead of variable name", func(r *ConfigurationRequest) {
			r.Profile.Credential = config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "secret-must-not-appear"}}
		}},
		{"unknown option", func(r *ConfigurationRequest) {
			r.Profile.Options = map[string]string{"secret-must-not-appear": "anything"}
		}},
		{"costs without table", func(r *ConfigurationRequest) { r.Features = []string{"costs"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := testRequest()
			tc.mutate(&request)
			plan, _, err := PlanConfiguration(filepath.Join(t.TempDir(), "config.yaml"), request)
			var detail *InputError
			if !errors.As(err, &detail) || detail.Code == "" || detail.Field == "" || plan.ID != "" {
				t.Fatalf("invalid request = %#v, err = %v", plan, err)
			}
			if strings.Contains(err.Error(), "secret-must-not-appear") {
				t.Fatalf("input value echoed: %v", err)
			}
		})
	}
}

func TestConfigurationFeaturesReuseProviderPrerequisites(t *testing.T) {
	request := testRequest()
	request.Features = []string{"costs", "metrics"}
	request.Profile.Options = map[string]string{"billing_table": "billing.export.daily"}
	prepared, err := PrepareRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Profile.Services, []string{"compute", "monitoring", "cost"}) || !reflect.DeepEqual(request.Profile.Services, []string{"compute"}) {
		t.Fatalf("services = %v, input = %v", prepared.Profile.Services, request.Profile.Services)
	}
	request.Profile.Scopes = config.Scopes{}
	request.Profile.Options["asset_scope"] = "folders/123"
	if _, err := PrepareRequest(request); err == nil {
		t.Fatal("folder-only billing accepted")
	}
	request.Features = []string{"resources"}
	if _, err := PrepareRequest(request); err != nil {
		t.Fatalf("folder resources rejected: %v", err)
	}
}

func TestConfigurationRequestUsesExistingSchemaForSevenClouds(t *testing.T) {
	for _, cloud := range model.Providers {
		t.Run(string(cloud), func(t *testing.T) {
			p := config.Profile{Provider: cloud, Credential: config.Credential{Source: "default"}, Regions: []string{"us-east-1"}, Services: []string{"compute"}, Scopes: config.Scopes{Accounts: []string{"123456789012"}}}
			switch cloud {
			case model.ProviderGCP:
				p.Scopes = config.Scopes{Projects: []string{"project-a"}}
				p.Regions = []string{"us-central1"}
			case model.ProviderAzure:
				p.Scopes = config.Scopes{Subscriptions: []string{"sub-a"}}
				p.Regions = []string{"eastus"}
			case model.ProviderAlibaba:
				p.Regions = []string{"cn-hangzhou"}
			case model.ProviderTencent:
				p.Regions = []string{"ap-guangzhou"}
			case model.ProviderVolcengine:
				p.Regions = []string{"cn-beijing"}
			case model.ProviderHuawei:
				p.Credential.Source = "env"
				p.Scopes = config.Scopes{Projects: []string{"project-a"}}
				p.Regions = []string{"cn-north-4"}
			}
			request := ConfigurationRequest{Name: "agent", Profile: p}
			data, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := ReadConfigurationRequest(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			plan, _, err := PlanConfiguration(path, decoded)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyConfiguration(path, decoded, plan.ID); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil || !reflect.DeepEqual(cfg.Profiles["agent"], p) {
				t.Fatalf("roundtrip = %#v, err = %v", cfg, err)
			}
		})
	}
}

func TestConfigurationJSONBoundaryDoesNotEchoRejectedData(t *testing.T) {
	for _, data := range []string{`{"secret-must-not-appear":"token"}`, `{"name":"agent","profile":{"credential":{"secret-must-not-appear":"token"}}}`, `{"name":`, `{} {}`, strings.Repeat("x", maxRequestBytes+1)} {
		_, err := ReadConfigurationRequest(strings.NewReader(data))
		var detail *InputError
		if !errors.As(err, &detail) || strings.Contains(err.Error(), "secret-must-not-appear") {
			t.Fatalf("unsafe or missing error: %v", err)
		}
	}
}

func TestConfigurationBillingNeedsItsOwnScope(t *testing.T) {
	request := ConfigurationRequest{Name: "huawei", Profile: config.Profile{Provider: model.ProviderHuawei, Credential: config.Credential{Source: "env"}, Scopes: config.Scopes{Projects: []string{"project-a"}}, Regions: []string{"cn-north-4"}, Services: []string{"compute"}, Options: map[string]string{"billing_site": "china"}}, Features: []string{"costs"}}
	if _, err := PrepareRequest(request); err == nil {
		t.Fatal("billing accepted without account scope")
	}
	request.Profile.Scopes.Accounts = []string{"account-a"}
	if _, err := PrepareRequest(request); err != nil {
		t.Fatal(err)
	}
}
