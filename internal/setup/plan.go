package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mcpcloud/internal/config"
	"mcpcloud/internal/provider"
)

var ErrConfigurationChanged = errors.New("configuration changed; create a new preview")
var ErrProfileExists = errors.New("profile already exists; choose another name")

// ConfigurationPlan binds a reviewed profile to a destination and its previous
// contents. ID detects stale input; it is not a user-approval credential.
type ConfigurationPlan struct {
	ID           string               `json:"id"`
	ConfigPath   string               `json:"config_path"`
	Request      ConfigurationRequest `json:"request"`
	baseRevision string
}

func configRevision(data []byte, exists bool) string {
	hash := sha256.New()
	if exists {
		hash.Write([]byte("existing\x00"))
	} else {
		hash.Write([]byte("missing\x00"))
	}
	hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func readConfigRevision(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("configuration must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	return configRevision(data, err == nil), nil
}

// PlanConfiguration validates an additive change without writing files or
// contacting cloud services. The returned candidate can be checked by Diagnose.
func PlanConfiguration(path string, request ConfigurationRequest) (ConfigurationPlan, config.Config, error) {
	var plan ConfigurationPlan
	var candidate config.Config
	request, err := PrepareRequest(request)
	if err != nil {
		return plan, candidate, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return plan, candidate, inputError("invalid_config_path", "config", "Cannot resolve the configuration path.")
	}
	revision, err := readConfigRevision(absolute)
	if err != nil {
		return plan, candidate, inputError("config_unreadable", "config", "Configuration must be an accessible regular file or a new path.")
	}
	candidate, err = config.Load(absolute)
	if err != nil {
		return plan, candidate, inputError("invalid_existing_config", "config", "Existing configuration is invalid; it was not changed.")
	}
	if _, exists := candidate.Profiles[request.Name]; exists {
		return plan, candidate, ErrProfileExists
	}
	if candidate.Profiles == nil {
		candidate.Profiles = map[string]config.Profile{}
	}
	candidate.Profiles[request.Name] = request.Profile
	if err := candidate.Validate(); err != nil {
		return plan, candidate, inputError("invalid_existing_config", "config", "Combined configuration is invalid; it was not changed.")
	}
	if _, err := provider.FromConfig(candidate); err != nil {
		return plan, candidate, inputError("invalid_existing_config", "config", "Existing provider configuration is invalid; it was not changed.")
	}
	current, err := readConfigRevision(absolute)
	if err != nil || revision != current {
		return plan, candidate, ErrConfigurationChanged
	}
	plan = ConfigurationPlan{ConfigPath: absolute, Request: request, baseRevision: revision}
	encoded, err := json.Marshal(struct {
		Version    int
		ConfigPath string
		Revision   string
		Request    ConfigurationRequest
	}{1, absolute, revision, request})
	if err != nil {
		return ConfigurationPlan{}, candidate, err
	}
	digest := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(digest[:])
	return plan, candidate, nil
}

// ApplyConfiguration recalculates the plan and checks the same file revision
// again under SaveProfile's writer lock. A changed request or file needs review.
func ApplyConfiguration(path string, request ConfigurationRequest, expectedID string) (ConfigurationPlan, error) {
	if expectedID == "" {
		return ConfigurationPlan{}, inputError("plan_id_required", "plan_id", "Apply requires the plan ID returned by a previous preview.")
	}
	plan, _, err := PlanConfiguration(path, request)
	if err != nil {
		return ConfigurationPlan{}, err
	}
	if plan.ID != expectedID {
		return ConfigurationPlan{}, ErrConfigurationChanged
	}
	if err := saveProfile(plan.ConfigPath, plan.Request.Name, plan.Request.Profile, &plan.baseRevision); err != nil {
		return ConfigurationPlan{}, err
	}
	return plan, nil
}
