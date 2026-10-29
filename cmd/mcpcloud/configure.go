package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/model"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/providers"
	"mcpcloud/internal/setup"
)

type configureResult struct {
	Status          string                   `json:"status"`
	Saved           bool                     `json:"saved"`
	RestartRequired bool                     `json:"restart_required"`
	Plan            *setup.ConfigurationPlan `json:"plan,omitempty"`
	Doctor          *setup.DoctorReport      `json:"doctor,omitempty"`
	Discovery       *setup.Discovery         `json:"discovery,omitempty"`
	Error           *setup.InputError        `json:"error,omitempty"`
}

func configure(args []string, input io.Reader, output io.Writer) error {
	result := configureResult{Status: "error"}
	encode := func(err error) error {
		if err != nil {
			var detail *setup.InputError
			switch {
			case errors.As(err, &detail):
				result.Error = detail
			case errors.Is(err, setup.ErrConfigurationChanged):
				result.Error = &setup.InputError{Code: "plan_changed", Field: "plan_id", Message: "The request, configuration or destination changed. Create a new preview and review it again."}
			case errors.Is(err, setup.ErrProfileExists):
				result.Error = &setup.InputError{Code: "profile_exists", Field: "name", Message: "This profile already exists. Inspect it or choose another name; configure never overwrites profiles."}
			default:
				result.Error = &setup.InputError{Code: "configuration_failed", Message: "Configuration could not be completed. Check file access or a concurrent writer and retry."}
			}
		}
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		if writeErr := encoder.Encode(result); writeErr != nil {
			return writeErr
		}
		if result.Error != nil {
			return result.Error
		}
		return nil
	}
	fail := func(code, field, message string) error {
		return encode(&setup.InputError{Code: code, Field: field, Message: message})
	}
	flags := flag.NewFlagSet("configure", flag.ContinueOnError)
	// A flag parser can quote rejected arguments, including accidentally pasted
	// credentials. Emit only our fixed diagnostics for this machine-facing command.
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "configuration destination (default: standard user config)")
	source := flags.String("input", "", "JSON request file, or - for stdin (required)")
	apply := flags.Bool("apply", false, "save the previously reviewed request, then run doctor")
	planID := flags.String("plan-id", "", "plan ID from preview; required with --apply")
	discover := flags.Bool("discover", false, "discover scope candidates using the request's credential references; never save")
	offline := flags.Bool("offline", false, "skip cloud calls in the post-save doctor (preview is always offline)")
	timeout := flags.Duration("timeout", 30*time.Second, "discovery or post-save doctor timeout, at most 2m")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(output)
			flags.PrintDefaults()
			return nil
		}
		return fail("invalid_arguments", "arguments", "Invalid configure arguments; use configure --help.")
	}
	if flags.NArg() != 0 || *source == "" || (*apply && *discover) || (*discover && *offline) || (*planID != "" && !*apply) {
		return fail("invalid_arguments", "arguments", "Provide --input; --apply and --discover are exclusive, --discover cannot be offline, and --plan-id requires --apply.")
	}
	if err := setupTimeout(*timeout); err != nil {
		return fail("invalid_timeout", "timeout", "Timeout must be greater than zero and no more than 2m.")
	}
	if *apply && *planID == "" {
		return fail("plan_id_required", "plan_id", "Apply requires the plan ID returned by a previous preview.")
	}
	reader := input
	if *source != "-" {
		file, err := os.Open(*source)
		if err != nil {
			return fail("input_unreadable", "input", "Cannot open the JSON request file.")
		}
		defer file.Close()
		reader = file
	}
	request, err := setup.ReadConfigurationRequest(reader)
	if err != nil {
		return encode(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *discover {
		if err := setup.ValidateIdentity(request); err != nil {
			return encode(err)
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		discovered, err := providers.DiscoverSetup(ctx, request.Profile)
		if err != nil {
			return fail("discovery_failed", "profile.credential", "Scope discovery failed. Check the existing identity and network, or enter explicit scope IDs manually.")
		}
		result.Status = "discovered"
		if request.Profile.Provider == model.ProviderHuawei || len(discovered.Scopes) == 0 {
			result.Status = "manual_scope_required"
		}
		result.Discovery = &setup.Discovery{Scopes: discovered.Scopes, Region: discovered.Region, Incomplete: discovered.Incomplete}
		return encode(nil)
	}
	absolute, err := setupPath(*path)
	if err != nil {
		return fail("invalid_config_path", "config", "Cannot resolve the configuration destination.")
	}
	if !*apply {
		plan, candidate, err := setup.PlanConfiguration(absolute, request)
		if err != nil {
			return encode(err)
		}
		report, err := runDiagnosis(ctx, candidate, []string{request.Name}, true, *timeout)
		if err != nil {
			return fail("doctor_failed", "doctor", "Local preview checks could not complete. No configuration was saved.")
		}
		result.Status = "preview"
		result.Plan = &plan
		result.Doctor = &report
		return encode(nil)
	}
	if ctx.Err() != nil {
		return fail("canceled", "apply", "Configuration was canceled before saving.")
	}
	plan, err := setup.ApplyConfiguration(absolute, request, *planID)
	if err != nil {
		return encode(err)
	}
	result.Status = "saved"
	result.Saved = true
	result.RestartRequired = true
	result.Plan = &plan
	cfg, err := config.Load(plan.ConfigPath)
	if err != nil {
		return fail("post_save_config_unreadable", "config", "The profile was saved, but configuration could not be reloaded for doctor.")
	}
	report, err := runDiagnosis(ctx, cfg, []string{request.Name}, *offline, *timeout)
	if err != nil {
		return fail("doctor_failed", "doctor", "The profile was saved, but doctor could not complete. Run doctor separately.")
	}
	result.Doctor = &report
	if report.Status != probe.StatusReady && report.Status != "offline" {
		return fail("doctor_not_ready", "doctor", "The profile was saved. Follow doctor next steps; do not retry applying the same profile.")
	}
	return encode(nil)
}
