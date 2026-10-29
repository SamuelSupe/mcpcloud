package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/probe"
)

type reportRunnerFunc func(context.Context, []string) (probe.Report, error)

func (f reportRunnerFunc) Run(ctx context.Context, names []string) (probe.Report, error) {
	return f(ctx, names)
}

func diagnosisCheck(t *testing.T, p ProfileDiagnosis, name string) Check {
	t.Helper()
	for _, c := range p.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing check %s", name)
	return Check{}
}

func TestDoctorSeparatesOptionalPrerequisitesFromVerifiedInventory(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles["cloud"] = testProfile()
	runner := reportRunnerFunc(func(_ context.Context, names []string) (probe.Report, error) {
		if !reflect.DeepEqual(names, []string{"cloud"}) {
			t.Fatalf("names = %v", names)
		}
		return probe.Report{Profiles: []probe.ProfileResult{{Profile: "cloud", Authenticated: true, ScopeVerified: true, Operation: "gcp.cloudasset.search_all_resources"}}}, nil
	})
	report, err := Diagnose(context.Background(), cfg, nil, false, runner)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != probe.StatusReady {
		t.Fatalf("optional costs blocked readiness: %#v", report)
	}
	for name, want := range map[string]string{"configuration": "pass", "credentials": "configured_unverified", "cloud_access": "pass", "scope": "pass", "metrics": "configured_unverified", "costs": "not_configured", "iam": "configured_unverified", "details": "not_checked"} {
		if got := diagnosisCheck(t, report.Profiles[0], name).Status; got != want {
			t.Errorf("%s = %s, want %s", name, got, want)
		}
	}
	if got := diagnosisCheck(t, report.Profiles[0], "cloud_access").Operation; got != "gcp.cloudasset.search_all_resources" {
		t.Fatalf("missing operation evidence: %s", got)
	}
}

func TestDoctorNeverTreatsEmptyOrFailedReadsAsScopeVerification(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles["cloud"] = testProfile()
	for _, tc := range []struct {
		name        string
		observation probe.ProfileResult
		want        string
		cloud       string
	}{
		{"empty inventory", probe.ProfileResult{Authenticated: true}, probe.StatusDegraded, "pass"},
		{"access denied", probe.ProfileResult{Error: &probe.SafeError{Code: "permission_denied"}}, probe.StatusNotReady, "fail"},
		{"timeout", probe.ProfileResult{Error: &probe.SafeError{Code: "timeout", Retryable: true}}, probe.StatusNotReady, "fail"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.observation.Profile = "cloud"
			report, err := Diagnose(context.Background(), cfg, nil, false, reportRunnerFunc(func(context.Context, []string) (probe.Report, error) {
				return probe.Report{Profiles: []probe.ProfileResult{tc.observation}}, nil
			}))
			if err != nil || report.Status != tc.want {
				t.Fatalf("report = %#v, err = %v", report, err)
			}
			if check := diagnosisCheck(t, report.Profiles[0], "scope"); check.Status != "unverified" || check.NextStep == "" {
				t.Fatalf("scope = %#v", check)
			}
			if check := diagnosisCheck(t, report.Profiles[0], "cloud_access"); check.Status != tc.cloud || (tc.cloud == "fail" && check.NextStep == "") {
				t.Fatalf("cloud = %#v", check)
			}
		})
	}
}

func TestDoctorOfflineAndMissingCredentialsDoNotContactCloud(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles["default"] = testProfile()
	calls := 0
	runner := reportRunnerFunc(func(_ context.Context, names []string) (probe.Report, error) {
		calls++
		if !reflect.DeepEqual(names, []string{"default"}) {
			t.Fatalf("unexpected cloud targets: %v", names)
		}
		return probe.Report{Profiles: []probe.ProfileResult{{Profile: "default", Authenticated: true, ScopeVerified: true}}}, nil
	})
	report, err := Diagnose(context.Background(), cfg, nil, true, runner)
	if err != nil || report.Status != "offline" || calls != 0 {
		t.Fatalf("offline report = %#v, err = %v, calls = %d", report, err, calls)
	}
	if diagnosisCheck(t, report.Profiles[0], "cloud_access").Status != "not_checked" {
		t.Fatal("offline implied authentication")
	}
	missing := config.Profile{Provider: model.ProviderTencent, Credential: config.Credential{Source: "env", Env: map[string]string{"TENCENTCLOUD_SECRET_ID": "SETUP_TEST_ID", "TENCENTCLOUD_SECRET_KEY": "SETUP_TEST_KEY"}}, Scopes: config.Scopes{Accounts: []string{"12345"}}, Regions: []string{"ap-guangzhou"}}
	t.Setenv("SETUP_TEST_ID", "")
	t.Setenv("SETUP_TEST_KEY", "")
	cfg.Profiles["missing"] = missing
	report, err = Diagnose(context.Background(), cfg, nil, false, runner)
	if err != nil || report.Status != probe.StatusNotReady || calls != 1 {
		t.Fatalf("missing report = %#v, err = %v, calls = %d", report, err, calls)
	}
	if diagnosisCheck(t, report.Profiles[1], "credentials").Status != "fail" {
		t.Fatal("missing credentials not reported")
	}
}

func TestDoctorGCPUnreadableCredentialFileIsALocalFailure(t *testing.T) {
	cfg := config.Default()
	p := testProfile()
	p.Credential = config.Credential{Source: "env", Env: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "SETUP_TEST_GCP_FILE"}}
	cfg.Profiles["cloud"] = p
	t.Setenv("SETUP_TEST_GCP_FILE", t.TempDir())
	report, err := Diagnose(context.Background(), cfg, nil, true, nil)
	if err != nil || report.Status != probe.StatusNotReady {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
}

func TestDoctorRejectsInvalidSelectionsBeforeProbeAndSanitizesErrors(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles["cloud"] = testProfile()
	disabled := testProfile()
	enabled := false
	disabled.Enabled = &enabled
	cfg.Profiles["disabled"] = disabled
	calls := 0
	runner := reportRunnerFunc(func(context.Context, []string) (probe.Report, error) {
		calls++
		return probe.Report{}, errors.New("secret-provider-message")
	})
	for _, names := range [][]string{{"absent"}, {"disabled"}, {"cloud", "cloud"}} {
		if _, err := Diagnose(context.Background(), cfg, names, false, runner); err == nil {
			t.Fatalf("selection accepted: %v", names)
		}
	}
	if calls != 0 {
		t.Fatal("invalid selection triggered cloud read")
	}
	if _, err := Diagnose(context.Background(), cfg, nil, false, runner); err == nil || strings.Contains(err.Error(), "secret-provider-message") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
	report, err := Diagnose(context.Background(), cfg, nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DoctorReport
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, decoded) {
		t.Fatal("JSON report did not roundtrip")
	}
	var output bytes.Buffer
	if err := PrintDoctor(&output, report); err != nil || output.Len() == 0 {
		t.Fatalf("human report: %v", err)
	}
}
