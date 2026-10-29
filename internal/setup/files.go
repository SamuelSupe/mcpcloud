package setup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

// SaveProfile adds a profile without replacing existing profiles or settings.
// The lock serializes writers using this helper; a content check detects edits
// made while the new configuration is being prepared.
func SaveProfile(path, name string, profile config.Profile) error {
	return saveProfile(path, name, profile, nil)
}

func saveProfile(path, name string, profile config.Profile, expectedRevision *string) error {
	unlock, err := lockConfiguration(path)
	if err != nil {
		return err
	}
	defer unlock()
	original, existed, err := readConfiguration(path)
	if err != nil {
		return err
	}
	if expectedRevision != nil && configRevision(original, existed) != *expectedRevision {
		return ErrConfigurationChanged
	}
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("existing configuration is invalid; it was not changed")
	}
	if _, exists := cfg.Profiles[name]; exists {
		return ErrProfileExists
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	cfg.Profiles[name] = profile
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := provider.FromConfig(cfg); err != nil {
		return err
	}
	var document yaml.Node
	if len(bytes.TrimSpace(original)) > 0 {
		if err := yaml.Unmarshal(original, &document); err != nil {
			return fmt.Errorf("cannot preserve existing configuration")
		}
	} else {
		document = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("configuration must be a YAML mapping")
	}
	root := document.Content[0]
	var profiles *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "profiles" {
			profiles = root.Content[i+1]
			break
		}
	}
	if profiles == nil {
		profiles = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "profiles"}, profiles)
	}
	if profiles.Tag == "!!null" {
		*profiles = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if profiles.Kind != yaml.MappingNode {
		return fmt.Errorf("profiles must be a mapping, not a YAML alias")
	}
	var value yaml.Node
	if err := value.Encode(profile); err != nil {
		return err
	}
	profiles.Content = append(profiles.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, &value)
	var content bytes.Buffer
	encoder := yaml.NewEncoder(&content)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return replaceConfiguration(path, original, existed, content.Bytes())
}

func lockConfiguration(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("configuration is locked or cannot be written: %w", err)
	}
	lock.Close()
	return func() { os.Remove(path + ".lock") }, nil
}

func readConfiguration(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("configuration must be a regular file, not a symlink")
	}
	content, err := os.ReadFile(path)
	return content, true, err
}

// replaceConfiguration uses the same atomic save and concurrent-edit check for
// cloud profiles and client registrations. Writers using this helper hold a lock.
func replaceConfiguration(path string, original []byte, existed bool, content []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mcpcloud-*")
	if err != nil {
		return err
	}
	tempName := temporary.Name()
	defer os.Remove(tempName)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	current, existsNow, readErr := readConfiguration(path)
	if readErr != nil || existed != existsNow || !bytes.Equal(original, current) {
		return ErrConfigurationChanged
	}
	return os.Rename(tempName, path)
}

// WriteNewFile never overwrites a client's existing configuration.
func WriteNewFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		os.Remove(path)
		return writeErr
	}
	return closeErr
}
