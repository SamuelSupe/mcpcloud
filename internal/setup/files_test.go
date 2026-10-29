package setup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
)

func testProfile() config.Profile {
	return config.Profile{Provider: model.ProviderGCP, Credential: config.Credential{Source: "default"}, Scopes: config.Scopes{Projects: []string{"project-a"}}, Regions: []string{"us-central1"}, Services: []string{"compute"}}
}

func TestSaveProfilePreservesExistingConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := SaveProfile(path, "first", testProfile()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content = append([]byte("# Keep this deployment note\nserver:\n  listen: 127.0.0.1:9090\nlimits:\n  concurrency: 2\n"), content...)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second := testProfile()
	second.Scopes.Projects = []string{"project-b"}
	if err := SaveProfile(path, "second", second); err != nil {
		t.Fatal(err)
	}
	after, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Profiles["first"], after.Profiles["first"]) || !reflect.DeepEqual(before.Server, after.Server) || before.Limits != after.Limits {
		t.Fatalf("existing settings changed: %#v", after)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("# Keep this deployment note")) {
		t.Fatal("comment lost")
	}
	for _, p := range []config.Profile{second, {Provider: model.ProviderGCP, Credential: config.Credential{Source: "invalid"}}} {
		if err := SaveProfile(path, "second", p); err == nil {
			t.Fatal("overwrote existing profile")
		}
		current, _ := os.ReadFile(path)
		if !bytes.Equal(content, current) {
			t.Fatal("configuration changed on failure")
		}
	}
	invalid := second
	invalid.Credential.Source = "invalid"
	if err := SaveProfile(path, "invalid", invalid); err == nil {
		t.Fatal("invalid profile saved")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(content, current) {
		t.Fatal("invalid write changed file")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode = %v", info.Mode())
		}
	}
}

func TestSaveProfileHandlesNullAndRejectsLocksAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("profiles: null\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, "one", testProfile()); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, "two", testProfile()); err == nil {
		t.Fatal("ignored writer lock")
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(original, current) {
		t.Fatal("locked file changed")
	}
	alias := filepath.Join(dir, "alias.yaml")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := SaveProfile(alias, "two", testProfile()); err == nil {
		t.Fatal("replaced symlink")
	}
	current, _ = os.ReadFile(path)
	if !bytes.Equal(original, current) {
		t.Fatal("symlink target changed")
	}
}

func TestClientConfigUsesLiteralAbsoluteArgumentsAndDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "MCP Cloud executable")
	path := filepath.Join(dir, "config space.yaml")
	if err := os.WriteFile(binary, []byte("binary fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(path, "cloud", testProfile()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "secret-path-must-not-be-copied")
	data, err := ClientConfig(binary, path, "my-cloud")
	if err != nil {
		t.Fatal(err)
	}
	var snippet struct {
		Servers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &snippet); err != nil {
		t.Fatal(err)
	}
	server := snippet.Servers["my-cloud"]
	if server.Command != binary || !reflect.DeepEqual(server.Args, []string{"serve", "--transport", "stdio", "--config", path}) || len(server.Env) != 0 {
		t.Fatalf("snippet = %#v", snippet)
	}
	if strings.Contains(string(data), "secret-path") {
		t.Fatal("environment value copied")
	}
	output := filepath.Join(dir, "client.json")
	if err := WriteNewFile(output, data); err != nil {
		t.Fatal(err)
	}
	if err := WriteNewFile(output, []byte("replacement")); err == nil {
		t.Fatal("overwrote existing client config")
	}
	current, _ := os.ReadFile(output)
	if !bytes.Equal(data, current) {
		t.Fatal("client file changed")
	}
	if _, err := ClientConfig(filepath.Join(dir, "missing"), path, "cloud"); err == nil {
		t.Fatal("missing executable accepted")
	}
	if _, err := ClientConfig(binary, filepath.Join(dir, "missing"), "cloud"); err == nil {
		t.Fatal("missing config accepted")
	}
}
