package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// setupSmoke exercises the generated client arguments against the actual binary,
// using an offline profile so release validation never needs cloud credentials.
func setupSmoke() error {
	dir, err := os.MkdirTemp("", "mcpcloud-setup-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config with spaces.yaml")
	init := cmd("init", "--offline", "--config", path)
	init.Stdin = strings.NewReader("gcp\n\ndefault\nprojects\nsetup-smoke-project\nus-central1\n\n\ny\n")
	if output, err := init.CombinedOutput(); err != nil {
		return fmt.Errorf("offline init failed: %w: %s", err, output)
	}
	output, err := cmd("doctor", "--offline", "--json", "--config", path).Output()
	if err != nil {
		return fmt.Errorf("offline doctor failed: %w", err)
	}
	var diagnosis struct {
		Status  string `json:"status"`
		Offline bool   `json:"offline"`
	}
	if err := json.Unmarshal(output, &diagnosis); err != nil {
		return err
	}
	if diagnosis.Status != "offline" || !diagnosis.Offline {
		return fmt.Errorf("offline doctor claimed cloud verification")
	}
	if err := configureSmoke(dir, path); err != nil {
		return err
	}
	output, err = cmd("client-config", "--config", path).Output()
	if err != nil {
		return fmt.Errorf("client config generation failed: %w", err)
	}
	var snippet struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(output, &snippet); err != nil {
		return err
	}
	server := snippet.Servers["mcpcloud"]
	if server.Command != binary || !reflect.DeepEqual(server.Args, []string{"serve", "--transport", "stdio", "--config", path}) || len(server.Env) != 0 {
		return fmt.Errorf("generated client config does not match executable/configuration")
	}
	if err := stdioCommand(cmd(server.Args...)); err != nil {
		return fmt.Errorf("generated MCP client launch failed: %w", err)
	}
	checks = append(checks, "offline init/doctor/client-config and generated stdio MCP launch")
	return codexSmoke(dir, path)
}

func codexSmoke(dir, configPath string) error {
	path := filepath.Join(dir, "codex config.toml")
	original := []byte("# Keep existing client settings\n[mcp_servers.other]\nurl = 'https://example.test/mcp'\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		return err
	}
	var installed []byte
	for _, status := range []string{"installed", "unchanged"} {
		output, err := cmd("client-config", "--client", "codex", "--install", "--codex-config", path, "--config", configPath).Output()
		if err != nil {
			return fmt.Errorf("Codex registration failed: %w", err)
		}
		var result struct {
			Status             string `json:"status"`
			RestartRequired    bool   `json:"restart_required"`
			ConnectionVerified bool   `json:"connection_verified"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			return err
		}
		if result.Status != status || !result.RestartRequired || result.ConnectionVerified {
			return fmt.Errorf("Codex registration reported an incorrect state")
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(current), string(original)) || (installed != nil && string(current) != string(installed)) {
			return fmt.Errorf("Codex registration changed existing settings")
		}
		installed = current
	}
	var cfg struct {
		Servers map[string]struct {
			Command string   `toml:"command"`
			Args    []string `toml:"args"`
			EnvVars []string `toml:"env_vars"`
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal(installed, &cfg); err != nil {
		return err
	}
	server := cfg.Servers["mcpcloud"]
	if server.Command != binary || !reflect.DeepEqual(server.Args, []string{"serve", "--transport", "stdio", "--config", configPath}) || !reflect.DeepEqual(server.EnvVars, []string{"GOOGLE_APPLICATION_CREDENTIALS"}) {
		return fmt.Errorf("Codex registration does not match executable/configuration")
	}
	if err := stdioCommand(cmd(server.Args...)); err != nil {
		return fmt.Errorf("Codex configuration MCP launch failed: %w", err)
	}
	checks = append(checks, "Codex native TOML merge, idempotent registration and generated stdio MCP launch")
	return nil
}

func configureSmoke(dir, configPath string) error {
	requestPath := filepath.Join(dir, "request.json")
	request := []byte(`{"name":"agent-profile","profile":{"provider":"gcp","credential":{"source":"default"},"scopes":{"projects":["agent-project"]},"regions":["us-central1"],"services":["compute"]},"features":["resources"]}`)
	if err := os.WriteFile(requestPath, request, 0600); err != nil {
		return err
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	output, err := cmd("configure", "--input", requestPath, "--config", configPath).Output()
	if err != nil {
		return fmt.Errorf("configure preview failed: %w", err)
	}
	var report struct {
		Status          string `json:"status"`
		Saved           bool   `json:"saved"`
		RestartRequired bool   `json:"restart_required"`
		Plan            struct {
			ID string `json:"id"`
		} `json:"plan"`
		Doctor struct {
			Status string `json:"status"`
		} `json:"doctor"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return err
	}
	if report.Status != "preview" || report.Saved || report.RestartRequired || report.Plan.ID == "" || report.Doctor.Status != "offline" {
		return fmt.Errorf("invalid configuration preview")
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	if string(before) != string(current) {
		return fmt.Errorf("preview changed configuration")
	}
	output, err = cmd("configure", "--input", requestPath, "--config", configPath, "--apply", "--plan-id", report.Plan.ID, "--offline").Output()
	if err != nil {
		return fmt.Errorf("configure apply failed: %w", err)
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return err
	}
	if report.Status != "saved" || !report.Saved || !report.RestartRequired || report.Doctor.Status != "offline" {
		return fmt.Errorf("invalid configuration apply report")
	}
	output, err = cmd("doctor", "--offline", "--json", "--config", configPath).Output()
	if err != nil {
		return err
	}
	var diagnosis struct {
		Profiles []struct {
			Profile string `json:"profile"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(output, &diagnosis); err != nil {
		return err
	}
	if len(diagnosis.Profiles) != 2 || diagnosis.Profiles[0].Profile != "agent-profile" || diagnosis.Profiles[1].Profile != "gcp-main" {
		return fmt.Errorf("configure did not preserve the existing profile")
	}
	checks = append(checks, "non-interactive configure preview/apply and preserved existing profile")
	return nil
}
