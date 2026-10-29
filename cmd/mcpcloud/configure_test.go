package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/setup"
)

const configureGCPRequest = `{"name":"agent-demo","profile":{"provider":"gcp","credential":{"source":"default"},"scopes":{"projects":["demo-project"]},"regions":["us-central1"],"services":["compute"]}}`

type configureNoNetwork struct{ t *testing.T }

func (r configureNoNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("offline configure contacted a cloud API")
	return nil, nil
}

func runConfigureTest(t *testing.T, args []string, input string) (configureResult, error) {
	t.Helper()
	var output bytes.Buffer
	err := configure(args, strings.NewReader(input), &output)
	var result configureResult
	if parseErr := json.Unmarshal(output.Bytes(), &result); parseErr != nil {
		t.Fatalf("configure did not return one JSON report: %s (%v)", output.String(), parseErr)
	}
	return result, err
}

func TestConfigurePreviewApplyAndDoctorReuseExistingFlow(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = configureNoNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = previous })
	path := filepath.Join(t.TempDir(), "config with spaces.yaml")
	args := []string{"--input", "-", "--config", path}
	preview, err := runConfigureTest(t, args, configureGCPRequest)
	if err != nil || preview.Status != "preview" || preview.Saved || preview.Plan == nil || preview.Doctor.Status != "offline" {
		t.Fatalf("preview = %#v, err = %v", preview, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("preview wrote config")
	}
	applied, err := runConfigureTest(t, append(args, "--apply", "--plan-id", preview.Plan.ID, "--offline"), configureGCPRequest)
	if err != nil || applied.Status != "saved" || !applied.Saved || !applied.RestartRequired || applied.Doctor.Status != "offline" {
		t.Fatalf("applied = %#v, err = %v", applied, err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Profiles["agent-demo"].Scopes.Projects[0] != "demo-project" {
		t.Fatalf("cfg = %#v, err = %v", cfg, err)
	}
	var doctorOutput bytes.Buffer
	if err := doctor([]string{"--config", path, "--profile", "agent-demo", "--offline", "--json"}, &doctorOutput); err != nil {
		t.Fatal(err)
	}
	var diagnosis setup.DoctorReport
	if err := json.Unmarshal(doctorOutput.Bytes(), &diagnosis); err != nil || diagnosis.Status != applied.Doctor.Status {
		t.Fatalf("doctor result mismatch: %v", err)
	}
	var clientOutput bytes.Buffer
	if err := clientConfig([]string{"--config", path}, &clientOutput); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(clientOutput.Bytes()) {
		t.Fatal("saved profile cannot generate client configuration")
	}
	duplicate, err := runConfigureTest(t, append(args, "--apply", "--plan-id", preview.Plan.ID, "--offline"), configureGCPRequest)
	if err == nil || duplicate.Saved || duplicate.Error.Code != "profile_exists" {
		t.Fatalf("duplicate = %#v, err = %v", duplicate, err)
	}
}

func TestConfigureSavedIsIndependentOfDoctorFailure(t *testing.T) {
	t.Setenv("AGENT_TEST_AK", "")
	t.Setenv("AGENT_TEST_SK", "")
	request := `{"name":"agent-demo","profile":{"provider":"tencent","credential":{"source":"env","env":{"TENCENTCLOUD_SECRET_ID":"AGENT_TEST_AK","TENCENTCLOUD_SECRET_KEY":"AGENT_TEST_SK"}},"scopes":{"accounts":["12345"]},"regions":["ap-guangzhou"],"services":["compute"]}}`
	path := filepath.Join(t.TempDir(), "config.yaml")
	args := []string{"--input", "-", "--config", path}
	preview, err := runConfigureTest(t, args, request)
	if err != nil || preview.Plan == nil || preview.Doctor.Status != "not_ready" {
		t.Fatalf("local missing env should allow preview: %#v, %v", preview, err)
	}
	applied, err := runConfigureTest(t, append(args, "--apply", "--plan-id", preview.Plan.ID, "--offline"), request)
	if err == nil || !applied.Saved || applied.Status != "saved" || applied.Doctor.Status != "not_ready" || applied.Error.Code != "doctor_not_ready" {
		t.Fatalf("apply result confused save and probe: %#v, %v", applied, err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Profiles["agent-demo"].Provider != model.ProviderTencent {
		t.Fatalf("profile lost after doctor failure: %v", err)
	}
}

func TestConfigureRejectsStalePlanAndMalformedInputWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	args := []string{"--input", "-", "--config", path}
	preview, err := runConfigureTest(t, args, configureGCPRequest)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.ReplaceAll(configureGCPRequest, "demo-project", "changed-project")
	result, err := runConfigureTest(t, append(args, "--apply", "--plan-id", preview.Plan.ID, "--offline"), changed)
	if err == nil || result.Saved || result.Error.Code != "plan_changed" {
		t.Fatalf("changed request accepted: %#v, %v", result, err)
	}
	for _, data := range []string{`{"secret-value-never-echo":"token"}`, `{"name":"ok","profile":{"provider":"secret-value-never-echo"}}`, `{} {}`} {
		result, err := runConfigureTest(t, args, data)
		encoded, _ := json.Marshal(result)
		if err == nil || result.Plan != nil || result.Saved || strings.Contains(string(encoded), "secret-value-never-echo") || strings.Contains(err.Error(), "secret-value-never-echo") {
			t.Fatalf("unsafe error output: %s, %v", encoded, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid request wrote config")
	}
}

func TestConfigureFileInputDiscoveryAndInvalidModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	identity := `{"name":"huawei-agent","profile":{"provider":"huawei","credential":{"source":"env"}}}`
	if err := os.WriteFile(path, []byte(identity), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := runConfigureTest(t, []string{"--input", path, "--discover"}, "")
	if err != nil || result.Status != "manual_scope_required" || result.Saved || result.Discovery == nil {
		t.Fatalf("manual discovery = %#v, %v", result, err)
	}
	for _, args := range [][]string{{}, {"--input", "-", "--apply"}, {"--input", "-", "--discover", "--offline"}, {"--input", "-", "--apply", "--discover"}, {"--input", "-", "--plan-id", "unused"}, {"--input", "-", "--timeout", "0s"}, {"--input", "-", "--secret-value-never-echo", "secret-value-never-echo"}} {
		result, err := runConfigureTest(t, args, identity)
		if err == nil || result.Saved || result.Error == nil || strings.Contains(err.Error(), "secret-value-never-echo") {
			t.Fatalf("invalid args = %#v, %v", result, err)
		}
	}
	// The command must dispatch to structured validation, even before input exists.
	if err := run([]string{"configure", "--input", filepath.Join(t.TempDir(), "absent")}); err == nil || strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("configure dispatch = %v", err)
	}
}

type unexpectedConfigureInput struct{ t *testing.T }

func (r unexpectedConfigureInput) Read([]byte) (int, error) {
	r.t.Fatal("missing plan ID caused input to be read")
	return 0, io.EOF
}

func TestConfigureMissingPlanDoesNotReadInput(t *testing.T) {
	var output bytes.Buffer
	if err := configure([]string{"--input", "-", "--apply"}, unexpectedConfigureInput{t}, &output); err == nil {
		t.Fatal("apply without review accepted")
	}
}

func TestConfigureDiscoversCandidatesWithoutSavingOrSelectingThem(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("Action") != "GetCallerIdentity" {
			t.Errorf("unexpected discovery action: %s", r.Form.Get("Action"))
		}
		w.Header().Set("Content-Type", "text/xml")
		io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>test</UserId><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`)
	}))
	defer server.Close()
	t.Setenv("AWS_ENDPOINT_URL_STS", server.URL)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "absent"))
	t.Setenv("AGENT_TEST_AWS_KEY", "test-access")
	t.Setenv("AGENT_TEST_AWS_SECRET", "secret-never-echo")
	input := `{"name":"aws-agent","profile":{"provider":"aws","credential":{"source":"env","env":{"AWS_ACCESS_KEY_ID":"AGENT_TEST_AWS_KEY","AWS_SECRET_ACCESS_KEY":"AGENT_TEST_AWS_SECRET"}}}}`
	path := filepath.Join(t.TempDir(), "config.yaml")
	result, err := runConfigureTest(t, []string{"--input", "-", "--config", path, "--discover"}, input)
	if err != nil || calls.Load() != 1 || result.Status != "discovered" || result.Saved || result.Plan != nil || result.Doctor != nil || result.Discovery == nil {
		t.Fatalf("discovery = %#v, calls = %d, err = %v", result, calls.Load(), err)
	}
	if len(result.Discovery.Scopes) != 1 || result.Discovery.Scopes[0] != "123456789012" {
		t.Fatalf("candidates = %#v", result.Discovery)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "secret-never-echo") {
		t.Fatal("credential copied into result")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("discovery saved a profile")
	}
}
