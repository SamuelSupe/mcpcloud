package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"

	"github.com/pelletier/go-toml/v2"
	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

// CodexClientConfig generates a native stdio table. Environment references are
// forwarded by name; values are never read or embedded in the client settings.
func CodexClientConfig(binary, configPath, name string, extraEnv []string) ([]byte, error) {
	if !setupNamePattern.MatchString(name) {
		return nil, fmt.Errorf("Codex server name must be 1-64 letters, digits, underscores or hyphens, starting with a letter or digit")
	}
	server, cfg, err := clientServer(binary, configPath)
	if err != nil {
		return nil, err
	}
	server.EnvVars, err = codexEnvironment(cfg, extraEnv)
	if err != nil {
		return nil, err
	}
	fields, err := toml.Marshal(server)
	if err != nil {
		return nil, err
	}
	// A child table can extend an existing mcp_servers table without redefining
	// its parent. The validated name contains no TOML syntax.
	return append([]byte("[mcp_servers."+name+"]\n"), fields...), nil
}

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,255}$`)

func codexEnvironment(cfg config.Config, extra []string) ([]string, error) {
	names := append([]string(nil), extra...)
	for _, profileName := range cfg.ProfileNames() {
		p := cfg.Profiles[profileName]
		if p.Credential.Source != "profile" {
			spec := credentialsFor(p.Provider)
			for _, group := range [][]string{spec.Required, spec.Optional} {
				for _, key := range group {
					name := p.Credential.Env[key]
					if name == "" {
						name = key
					}
					names = append(names, name)
				}
			}
		}
		if p.Provider == model.ProviderAWS {
			names = append(names, "AWS_CONFIG_FILE", "AWS_SHARED_CREDENTIALS_FILE")
			if p.Credential.Source == "default" || p.Credential.Source == "workload" {
				names = append(names, "AWS_PROFILE", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_SESSION_NAME")
			}
		}
		if p.Provider == model.ProviderAzure && p.Credential.Source != "env" {
			names = append(names, "AZURE_FEDERATED_TOKEN_FILE", "AZURE_AUTHORITY_HOST")
		}
	}
	for _, name := range names {
		if !environmentNamePattern.MatchString(name) {
			return nil, fmt.Errorf("environment references must be variable names, not assignments or values")
		}
	}
	sort.Strings(names)
	return slices.Compact(names), nil
}

// CodexConfigPath resolves an explicit destination or the user's Codex home.
// Project/profile-specific configuration is selected only by an explicit path.
func CodexConfigPath(path string) (string, error) {
	if path == "" {
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			home = filepath.Join(userHome, ".codex")
		}
		path = filepath.Join(home, "config.toml")
	}
	return filepath.Abs(path)
}

type CodexInstallResult struct {
	Status             string `json:"status"`
	ConfigPath         string `json:"config_path"`
	ServerName         string `json:"server_name"`
	Enabled            bool   `json:"enabled"`
	RestartRequired    bool   `json:"restart_required"`
	ConnectionVerified bool   `json:"connection_verified"`
}

// InstallCodex merges a generated table without rewriting existing TOML. A
// matching registration is a no-op, including its local policy and enabled flag.
// Conflicting registrations are never overwritten. This does not reload Codex.
func InstallCodex(path, name string, snippet []byte) (CodexInstallResult, error) {
	result := CodexInstallResult{}
	absolute, err := CodexConfigPath(path)
	if err != nil {
		return result, err
	}
	var desired map[string]any
	if err := toml.Unmarshal(snippet, &desired); err != nil || !setupNamePattern.MatchString(name) {
		return result, fmt.Errorf("invalid Codex configuration snippet")
	}
	servers, ok := desired["mcp_servers"].(map[string]any)
	wanted, exists := servers[name].(map[string]any)
	if !ok || !exists || len(desired) != 1 || len(servers) != 1 {
		return result, fmt.Errorf("expected one Codex MCP server table")
	}
	unlock, err := lockConfiguration(absolute)
	if err != nil {
		return result, err
	}
	defer unlock()
	original, existed, err := readConfiguration(absolute)
	if err != nil {
		return result, err
	}
	var current map[string]any
	if err := toml.Unmarshal(original, &current); err != nil {
		// TOML diagnostics can contain the offending line, including credentials.
		return result, fmt.Errorf("existing Codex TOML is invalid; it was not changed")
	}
	result = CodexInstallResult{Status: "unchanged", ConfigPath: absolute, ServerName: name, Enabled: true, RestartRequired: true}
	if raw, present := current["mcp_servers"]; present {
		existing, ok := raw.(map[string]any)
		if !ok {
			return CodexInstallResult{}, fmt.Errorf("Codex mcp_servers must be a table; configuration was not changed")
		}
		if raw, present := existing[name]; present {
			server, ok := raw.(map[string]any)
			if !ok || !sameCodexServer(server, wanted) {
				return CodexInstallResult{}, fmt.Errorf("Codex server name conflicts with existing settings; choose another --name or review that entry manually")
			}
			if enabled, ok := server["enabled"].(bool); ok {
				result.Enabled = enabled
			}
			return result, nil
		}
	}
	merged := append(bytes.Clone(original), '\n')
	merged = append(merged, snippet...)
	var verified map[string]any
	if err := toml.Unmarshal(merged, &verified); err != nil {
		return CodexInstallResult{}, fmt.Errorf("cannot extend Codex TOML without rewriting existing settings; use standard [mcp_servers.NAME] tables or a separate --codex-config path")
	}
	if err := replaceConfiguration(absolute, original, existed, merged); err != nil {
		return CodexInstallResult{}, err
	}
	result.Status = "installed"
	return result, nil
}

func sameCodexServer(current, wanted map[string]any) bool {
	if _, http := current["url"]; http {
		return false
	}
	if current["experimental_environment"] == "remote" {
		return false
	}
	if !reflect.DeepEqual(current["command"], wanted["command"]) || !reflect.DeepEqual(current["args"], wanted["args"]) {
		return false
	}
	// Keep additional forwarded variables and all per-server policy untouched.
	actual, _ := current["env_vars"].([]any)
	required, _ := wanted["env_vars"].([]any)
	for _, name := range required {
		found := false
		for _, value := range actual {
			if local, ok := value.(map[string]any); ok {
				value = nil
				if local["source"] == nil || local["source"] == "local" {
					value = local["name"]
				}
			}
			if reflect.DeepEqual(value, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
