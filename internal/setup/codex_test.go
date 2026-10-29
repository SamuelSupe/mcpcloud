package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

func TestCodexGenerationUsesValidatedPathsAndCredentialReferences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config space.yaml")
	binary := filepath.Join(dir, "MCP Cloud executable")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	p := testProfile()
	p.Credential = config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "CUSTOM_ADC_PATH"}}
	if err := SaveProfile(path, "gcp", p); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUSTOM_ADC_PATH", "secret-value-must-not-be-copied")
	snippet, err := CodexClientConfig(binary, path, "cloud-prod", []string{"CUSTOM_ADC_PATH", "EXTRA_IDENTITY_FILE"})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Servers map[string]stdioServer `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(snippet, &cfg); err != nil {
		t.Fatal(err)
	}
	server := cfg.Servers["cloud-prod"]
	if server.Command != binary || !reflect.DeepEqual(server.Args, []string{"serve", "--transport", "stdio", "--config", path}) || !reflect.DeepEqual(server.EnvVars, []string{"CUSTOM_ADC_PATH", "EXTRA_IDENTITY_FILE"}) {
		t.Fatalf("unexpected server: %#v", server)
	}
	if bytes.Contains(snippet, []byte("secret-value")) {
		t.Fatal("credential value leaked")
	}
	for _, invalid := range []struct{ name, env string }{{"cloud]\n[other", "OK"}, {"cloud", "TOKEN=secret-value"}, {"cloud", "*"}} {
		if _, err := CodexClientConfig(binary, path, invalid.name, []string{invalid.env}); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("unsafe generation error: %v", err)
		}
	}
	if _, err := CodexClientConfig(binary, filepath.Join(dir, "missing"), "cloud", nil); err == nil {
		t.Fatal("generated registration for missing cloud configuration")
	}
}

func TestCodexEnvironmentCoversCloudReferencesAndSkipsDisabledProfiles(t *testing.T) {
	cfg := config.Default()
	for _, cloud := range model.Providers {
		spec := credentialsFor(cloud)
		p := config.Profile{Provider: cloud, Credential: config.Credential{Source: "env", Env: map[string]string{}}}
		for _, key := range spec.Required {
			p.Credential.Env[key] = "CUSTOM_" + key
		}
		cfg.Profiles[string(cloud)] = p
	}
	disabled := false
	cfg.Profiles["disabled"] = config.Profile{Provider: model.ProviderGCP, Enabled: &disabled, Credential: config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "DISABLED_SECRET"}}}
	names, err := codexEnvironment(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cloud := range model.Providers {
		spec := credentialsFor(cloud)
		for _, key := range spec.Required {
			if !slices.Contains(names, "CUSTOM_"+key) || slices.Contains(names, key) {
				t.Fatalf("custom credential reference lost for %s", cloud)
			}
		}
		for _, key := range spec.Optional {
			if !slices.Contains(names, key) {
				t.Fatalf("optional credential reference lost for %s", cloud)
			}
		}
	}
	if slices.Contains(names, "DISABLED_SECRET") {
		t.Fatal("disabled profile forwarded credentials")
	}
	cfg.Profiles = map[string]config.Profile{"aws": {Provider: model.ProviderAWS, Credential: config.Credential{Source: "profile", Profile: "readonly"}}}
	names, err = codexEnvironment(cfg, nil)
	if err != nil || !reflect.DeepEqual(names, []string{"AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE"}) {
		t.Fatalf("shared AWS profile references: %v, %v", names, err)
	}
}

func TestInstallCodexPreservesSettingsAndIsIdempotent(t *testing.T) {
	snippet := []byte("[mcp_servers.cloud]\ncommand = '/bin/mcpcloud'\nargs = ['serve', '--config', '/config space.yaml']\nenv_vars = ['ADC_PATH']\n")
	original := []byte("# Keep all user settings\nmodel = 'my-model'\n[mcp_servers]\n[mcp_servers.\"other.server\"]\nurl = 'https://example.test/mcp' # keep comment\n[mcp_servers.\"other.server\".http_headers]\nAuthorization = 'existing-secret'\n[projects.\"/work/repo\"]\ntrust_level = 'trusted'\n")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, original, 0640); err != nil {
		t.Fatal(err)
	}
	result, err := InstallCodex(path, "cloud", snippet)
	if err != nil || result.Status != "installed" || !result.Enabled || !result.RestartRequired || result.ConnectionVerified {
		t.Fatalf("installation = %#v, %v", result, err)
	}
	installed, _ := os.ReadFile(path)
	if !bytes.HasPrefix(installed, original) {
		t.Fatal("existing settings/comments rewritten")
	}
	var before, after map[string]any
	if err := toml.Unmarshal(original, &before); err != nil {
		t.Fatal(err)
	}
	if err := toml.Unmarshal(installed, &after); err != nil {
		t.Fatal(err)
	}
	delete(after["mcp_servers"].(map[string]any), "cloud")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unrelated configuration semantics changed")
	}
	// User-local policies must survive repeated registration, including disable.
	installed = bytes.ReplaceAll(installed, []byte("env_vars = ['ADC_PATH']"), []byte("env_vars = [{name = 'ADC_PATH', source = 'local'}, 'EXTRA_VAR']"))
	installed = append(installed, []byte("enabled = false\ntool_timeout_sec = 90\n[ mcp_servers.cloud.env ]\nCUSTOM = 'existing-value'\n")...)
	if err := os.WriteFile(path, installed, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = InstallCodex(path, "cloud", snippet)
	if err != nil || result.Status != "unchanged" || result.Enabled {
		t.Fatalf("repeat installation = %#v, %v", result, err)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(installed, current) {
		t.Fatal("repeat install changed user settings")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe mode %v", info.Mode())
		}
	}
}

func TestCodexInstallRejectsConflictsWithoutChangingFile(t *testing.T) {
	snippet := []byte("[mcp_servers.cloud]\ncommand = '/bin/mcpcloud'\nargs = ['serve']\n")
	for _, original := range []string{
		"[mcp_servers.cloud]\nurl = 'https://example.test/mcp'\n",
		"[mcp_servers.cloud]\ncommand = '/other/program'\nargs = ['serve']\n",
		"[mcp_servers.cloud]\ncommand = '/bin/mcpcloud'\nargs = ['serve', '--config', '/other.yaml']\n",
		"[mcp_servers.cloud]\ncommand = '/bin/mcpcloud'\nargs = ['serve']\nexperimental_environment = 'remote'\n",
		"secret = 'secret-value\n",
		"mcp_servers = 7\n",
		"mcp_servers = { other = {command = 'other'} }\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallCodex(path, "cloud", snippet); err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid/conflicting settings accepted or exposed: %v", err)
		}
		current, _ := os.ReadFile(path)
		if string(current) != original {
			t.Fatal("failed installation changed configuration")
		}
	}
}

func TestCodexInstallPathAndWriteBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path, err := CodexConfigPath("")
	if err != nil || path != filepath.Join(home, "config.toml") {
		t.Fatalf("Codex home resolution: %q %v", path, err)
	}
	snippet := []byte("[mcp_servers.cloud]\ncommand = '/bin/mcpcloud'\nargs = ['serve']\n")
	result, err := InstallCodex("", "cloud", snippet)
	if err != nil || result.ConfigPath != path {
		t.Fatalf("new installation: %#v %v", result, err)
	}
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallCodex(path, "cloud", snippet); err == nil {
		t.Fatal("ignored writer lock")
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := replaceConfiguration(path, []byte("stale"), true, snippet); !errors.Is(err, ErrConfigurationChanged) {
		t.Fatalf("lost concurrent edit: %v", err)
	}
	alias := filepath.Join(home, "alias.toml")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := InstallCodex(alias, "cloud", snippet); err == nil {
		t.Fatal("replaced settings symlink")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(original, current) {
		t.Fatal("failed writes changed configuration")
	}
}
