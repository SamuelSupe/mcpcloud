package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mcpcloud/internal/config"
	"mcpcloud/internal/engine"
	"mcpcloud/internal/mcpserver"
	"mcpcloud/internal/probe"
	"mcpcloud/internal/provider"
	_ "mcpcloud/internal/providers"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mcpcloud <serve|validate-config|probe-profile> [options]")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "validate-config":
		return validateConfig(args[1:])
	case "probe-profile":
		return probeProfile(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func probeProfile(args []string) error {
	flags := flag.NewFlagSet("probe-profile", flag.ContinueOnError)
	configPath := flags.String("config", "", "configuration file path")
	profileName := flags.String("profile", "", "one configured profile name")
	all := flags.Bool("all", false, "probe all enabled profiles")
	timeout := flags.Duration("timeout", 30*time.Second, "total probe timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if (strings.TrimSpace(*profileName) == "") == !*all {
		return errors.New("provide exactly one of --profile or --all")
	}
	if *timeout <= 0 || *timeout > 2*time.Minute {
		return errors.New("--timeout must be greater than zero and no more than 2m")
	}
	cfg, err := load(*configPath)
	if err != nil {
		return err
	}
	registry, err := provider.FromConfig(cfg)
	if err != nil {
		return err
	}
	profiles := registry.Profiles()
	if !*all {
		profiles = []string{strings.TrimSpace(*profileName)}
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	queryEngine := engine.New(registry, cfg.Limits)
	queryEngine.SetAuditSink(engine.NewSlogAuditSink(logger))
	runner := probe.NewRunner(registry, queryEngine)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	report, err := runner.Run(ctx, profiles)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if report.Status != probe.StatusReady {
		return fmt.Errorf("profile probe completed with status %s", report.Status)
	}
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	transport := flags.String("transport", "stdio", "stdio or http")
	configPath := flags.String("config", "", "configuration file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := load(*configPath)
	if err != nil {
		return err
	}
	registry, err := provider.FromConfig(cfg)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server := mcpserver.New(cfg, registry, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch *transport {
	case "stdio":
		return server.RunStdio(ctx)
	case "http":
		return server.RunHTTP(ctx)
	default:
		return fmt.Errorf("transport must be stdio or http")
	}
}

func validateConfig(args []string) error {
	flags := flag.NewFlagSet("validate-config", flag.ContinueOnError)
	configPath := flags.String("config", "", "configuration file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	cfg, err := load(*configPath)
	if err != nil {
		return err
	}
	if _, err := provider.FromConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("configuration valid: %d enabled profile(s)\n", len(cfg.ProfileNames()))
	return nil
}

func load(path string) (config.Config, error) {
	if path == "" {
		defaultPath, err := config.DefaultPath()
		if err != nil {
			return config.Config{}, err
		}
		path = defaultPath
	}
	return config.Load(path)
}
