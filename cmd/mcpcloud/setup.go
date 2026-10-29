package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/engine"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/provider"
	"mcpcloud/internal/providers"
	"mcpcloud/internal/setup"
)

func setupPath(path string) (string, error) {
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(path)
}

func setupTimeout(timeout time.Duration) error {
	if timeout <= 0 || timeout > 2*time.Minute {
		return fmt.Errorf("--timeout must be greater than zero and no more than 2m")
	}
	return nil
}

func initConfig(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	path := flags.String("config", "", "configuration file path")
	offline := flags.Bool("offline", false, "skip cloud discovery and live checks")
	timeout := flags.Duration("timeout", 30*time.Second, "timeout for each discovery or doctor phase (not interactive input)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("usage: mcpcloud init [--config PATH] [--offline] [--timeout 30s]")
	}
	if err := setupTimeout(*timeout); err != nil {
		return err
	}
	absolute, err := setupPath(*path)
	if err != nil {
		return err
	}
	cfg, err := config.Load(absolute)
	if err != nil {
		return fmt.Errorf("existing configuration is invalid; it was not changed")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	wizard := setup.Wizard{Input: input, Output: output, Offline: *offline, Discover: func(ctx context.Context, p config.Profile) (setup.Discovery, error) {
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		result, err := providers.DiscoverSetup(ctx, p)
		return setup.Discovery{Scopes: result.Scopes, Region: result.Region, Incomplete: result.Incomplete}, err
	}}
	name, p, err := wizard.Run(ctx, cfg)
	if errors.Is(err, setup.ErrCanceled) {
		_, err = fmt.Fprintln(output, "已取消，配置未写入。")
		return err
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := setup.SaveProfile(absolute, name, p); err != nil {
		return err
	}
	fmt.Fprintf(output, "\n已保存 profile %s：%s\n", name, absolute)
	cfg, err = config.Load(absolute)
	if err != nil {
		return err
	}
	report, err := runDiagnosis(ctx, cfg, []string{name}, *offline, *timeout)
	if err != nil {
		return fmt.Errorf("profile was saved, but doctor could not run: %w", err)
	}
	if err := setup.PrintDoctor(output, report); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	snippet, err := setup.ClientConfig(binary, absolute, "mcpcloud")
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "\nMCP 客户端配置（mcpServers JSON；凭证由客户端启动的进程环境提供）：\n%s", snippet)
	if report.Status != probe.StatusReady && report.Status != "offline" {
		return fmt.Errorf("profile was saved; doctor status is %s, follow the reported next steps", report.Status)
	}
	return nil
}

func runDiagnosis(ctx context.Context, cfg config.Config, names []string, offline bool, timeout time.Duration) (setup.DoctorReport, error) {
	registry, err := provider.FromConfig(cfg)
	if err != nil {
		return setup.DoctorReport{}, fmt.Errorf("provider configuration is invalid; run validate-config")
	}
	runner := probe.NewRunner(registry, engine.New(registry, cfg.Limits))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return setup.Diagnose(ctx, cfg, names, offline, runner)
}

func doctor(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	path := flags.String("config", "", "configuration file path")
	name := flags.String("profile", "", "one enabled profile (default: all enabled profiles)")
	all := flags.Bool("all", false, "check all enabled profiles")
	offline := flags.Bool("offline", false, "check local prerequisites only; never contact cloud APIs")
	asJSON := flags.Bool("json", false, "print a structured diagnostic report")
	timeout := flags.Duration("timeout", 30*time.Second, "total cloud probe timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("doctor does not accept positional arguments")
	}
	if *all && strings.TrimSpace(*name) != "" {
		return fmt.Errorf("--profile and --all cannot be combined")
	}
	if err := setupTimeout(*timeout); err != nil {
		return err
	}
	cfg, err := load(*path)
	if err != nil {
		return fmt.Errorf("configuration is invalid or unreadable; run validate-config")
	}
	var names []string
	if strings.TrimSpace(*name) != "" {
		names = []string{strings.TrimSpace(*name)}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report, err := runDiagnosis(ctx, cfg, names, *offline, *timeout)
	if err != nil {
		return err
	}
	if *asJSON {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(report)
	} else {
		err = setup.PrintDoctor(output, report)
	}
	if err != nil {
		return err
	}
	if report.Status != probe.StatusReady && report.Status != "offline" {
		return fmt.Errorf("doctor completed with status %s", report.Status)
	}
	return nil
}
