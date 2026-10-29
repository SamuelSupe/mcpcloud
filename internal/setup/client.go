package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

type stdioServer struct {
	Command string   `json:"command" toml:"command"`
	Args    []string `json:"args" toml:"args"`
	EnvVars []string `json:"-" toml:"env_vars,omitempty"`
}

// ClientConfig returns a stdio snippet for clients using the mcpServers format.
// Credentials are inherited by the launched process, never copied into JSON.
func ClientConfig(binary, configPath, name string) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("client server name is required")
	}
	server, _, err := clientServer(binary, configPath)
	if err != nil {
		return nil, err
	}
	output := map[string]any{"mcpServers": map[string]any{name: server}}
	data, err := json.MarshalIndent(output, "", "  ")
	return append(data, '\n'), err
}

func clientServer(binary, configPath string) (stdioServer, config.Config, error) {
	var server stdioServer
	var cfg config.Config
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		return server, cfg, err
	}
	info, err := os.Stat(absoluteBinary)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return server, cfg, fmt.Errorf("binary must name an existing executable file")
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return server, cfg, err
	}
	info, err = os.Stat(absoluteConfig)
	if err != nil || !info.Mode().IsRegular() {
		return server, cfg, fmt.Errorf("configuration must name an existing regular file")
	}
	cfg, err = config.Load(absoluteConfig)
	if err != nil {
		return server, cfg, fmt.Errorf("configuration is invalid; run validate-config")
	}
	if len(cfg.ProfileNames()) == 0 {
		return server, cfg, fmt.Errorf("configuration has no enabled profiles")
	}
	if _, err := provider.FromConfig(cfg); err != nil {
		return server, cfg, fmt.Errorf("provider configuration is invalid; run validate-config")
	}
	server.Command = absoluteBinary
	server.Args = []string{"serve", "--transport", "stdio", "--config", absoluteConfig}
	return server, cfg, nil
}
