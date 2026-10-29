package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"mcpcloud/internal/config"
	"mcpcloud/internal/setup"
)

func TestOfflineSetupDoctorAndClientConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config space.yaml")
	answers := "gcp\n\ndefault\nprojects\nproject-a\nus-central1\n\n\ny\n"
	var output bytes.Buffer
	if err := initConfig([]string{"--offline", "--config", path}, strings.NewReader(answers), &output); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 1 || cfg.Profiles["gcp-main"].Scopes.Projects[0] != "project-a" {
		t.Fatalf("saved config = %#v", cfg)
	}
	original, _ := os.ReadFile(path)
	if err := initConfig([]string{"--offline", "--config", path}, strings.NewReader(answers), &bytes.Buffer{}); err == nil {
		t.Fatal("duplicate profile accepted")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(current, original) {
		t.Fatal("duplicate setup overwrote config")
	}
	output.Reset()
	if err := doctor([]string{"--offline", "--json", "--config", path}, &output); err != nil {
		t.Fatal(err)
	}
	var report setup.DoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "offline" || len(report.Profiles) != 1 {
		t.Fatalf("report = %#v", report)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := clientConfig([]string{"--config", path, "--binary", binary}, &output); err != nil {
		t.Fatal(err)
	}
	var snippet map[string]any
	if err := json.Unmarshal(output.Bytes(), &snippet); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "client.json")
	if err := clientConfig([]string{"--config", path, "--output", destination}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := clientConfig([]string{"--config", path, "--output", destination}, &bytes.Buffer{}); err == nil {
		t.Fatal("existing client file overwritten")
	}
	codexHome := filepath.Join(dir, "codex-home")
	t.Setenv("CODEX_HOME", codexHome)
	output.Reset()
	if err := clientConfig([]string{"--client", "codex", "--config", path, "--env-var", "CUSTOM_IDENTITY"}, &output); err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := toml.Unmarshal(output.Bytes(), &native); err != nil || native["mcp_servers"] == nil {
		t.Fatalf("invalid native configuration: %v", err)
	}
	if _, err := os.Stat(codexHome); !os.IsNotExist(err) {
		t.Fatal("generation wrote Codex settings")
	}
	for _, status := range []string{"installed", "unchanged"} {
		output.Reset()
		if err := clientConfig([]string{"--client", "codex", "--config", path, "--install", "--env-var", "CUSTOM_IDENTITY"}, &output); err != nil {
			t.Fatal(err)
		}
		var result setup.CodexInstallResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != status || !result.Enabled || !result.RestartRequired || result.ConnectionVerified || result.ConfigPath != filepath.Join(codexHome, "config.toml") {
			t.Fatalf("registration report: %#v", result)
		}
	}
	custom := filepath.Join(dir, "project", ".codex", "config.toml")
	if err := clientConfig([]string{"--client", "codex", "--config", path, "--install", "--codex-config", custom, "--name", "cloud-project"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal(err)
	}
}

func TestInitCancelAndEOFLeaveNoConfiguration(t *testing.T) {
	for _, answers := range []string{"gcp\n\ndefault\nprojects\nproject-a\nus-central1\n\n\nn\n", "gcp\n"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		_ = initConfig([]string{"--offline", "--config", path}, strings.NewReader(answers), &bytes.Buffer{})
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("canceled setup wrote a file: %v", err)
		}
	}
}

func TestSetupCommandsValidateArgumentsAndReportFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	for _, args := range [][]string{{"--all", "--profile", "p"}, {"--timeout", "0s"}, {"--timeout", "3m"}, {"extra"}, {"--config", path}} {
		if err := doctor(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("doctor accepted %v", args)
		}
	}
	if err := initConfig([]string{"--timeout", "0s"}, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid init timeout accepted")
	}
	if err := clientConfig([]string{"--config", path}, &bytes.Buffer{}); err == nil {
		t.Fatal("generated config for absent configuration")
	}
	for _, args := range [][]string{{"--client", "unknown"}, {"--install"}, {"--env-var", "TOKEN"}, {"--client", "codex", "--codex-config", path}, {"--client", "codex", "--install", "--output", path}} {
		if err := clientConfig(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("client-config accepted conflicting arguments %v", args)
		}
	}
	if err := run([]string{"doctor", "--offline", "--config", path}); err == nil || strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("doctor command not dispatched: %v", err)
	}
}

func TestDoctorMissingCredentialUsesNonzeroResultAndSafeJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "profiles:\n  cloud:\n    provider: tencent\n    credential:\n      source: env\n      env:\n        TENCENTCLOUD_SECRET_ID: SETUP_CLI_TEST_ID\n        TENCENTCLOUD_SECRET_KEY: SETUP_CLI_TEST_KEY\n    scopes:\n      accounts: ['12345']\n    regions: [ap-guangzhou]\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SETUP_CLI_TEST_ID", "secret-must-not-appear")
	t.Setenv("SETUP_CLI_TEST_KEY", "")
	var output bytes.Buffer
	if err := doctor([]string{"--offline", "--json", "--config", path}, &output); err == nil {
		t.Fatal("missing credential check succeeded")
	}
	var report setup.DoctorReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "not_ready" || strings.Contains(output.String(), "secret-must-not-appear") {
		t.Fatalf("unsafe or incorrect report: %s", output.String())
	}
}
