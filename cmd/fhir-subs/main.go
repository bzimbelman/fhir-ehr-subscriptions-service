// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command fhir-subs is the entry point for the fhir-ehr-subscriptions-service server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// defaultConfigPath is the canonical config-file location per the configuration LLD.
const defaultConfigPath = "/etc/fhir-subs/config.yaml"

// stringSlice is a flag.Value that collects repeated --set entries.
type stringSlice []string

func (s *stringSlice) String() string { return fmt.Sprint([]string(*s)) }

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// CliOptions captures parsed CLI flags. The merger applies these last (highest
// precedence) once the layered loader is real; for now main consumes them directly.
type CliOptions struct {
	ConfigPath  string
	LogLevel    string
	CheckConfig bool
	Sets        []string
}

// errHelpRequested is returned by parseFlags when the caller asked for --help.
// Main translates this into "print usage, exit 0".
var errHelpRequested = errors.New("help requested")

// errVersionRequested is returned by parseFlags when the caller asked for --version.
var errVersionRequested = errors.New("version requested")

// parseFlags is a deterministic, side-effect-free flag parser. It writes usage
// to out on --help, never to os.Stderr directly, so tests can capture output.
func parseFlags(args []string, out io.Writer) (*CliOptions, error) {
	fs := flag.NewFlagSet("fhir-subs", flag.ContinueOnError)
	fs.SetOutput(out)

	var (
		configPath  string
		logLevel    string
		checkConfig bool
		showVersion bool
		sets        stringSlice
	)

	fs.StringVar(&configPath, "config", defaultConfigPath, "path to the config file")
	fs.StringVar(&logLevel, "log-level", "", "override deployment.log_level (one of debug, info, warn, error)")
	fs.BoolVar(&checkConfig, "check-config", false, "validate the config file and exit")
	fs.BoolVar(&showVersion, "version", false, "print version and exit")
	fs.Var(&sets, "set", "override a config key (--set dotted.key=value); may repeat")

	fs.Usage = func() {
		fmt.Fprintf(out, "Usage of fhir-subs:\n")
		fmt.Fprintf(out, "  fhir-subs [--config PATH] [--log-level LEVEL] [--check-config] [--set KEY=VALUE]...\n")
		fmt.Fprintf(out, "  fhir-subs --version\n")
		fmt.Fprintf(out, "  fhir-subs --help\n\n")
		fmt.Fprintf(out, "Flags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError emits flag.ErrHelp for -h/--help.
		if errors.Is(err, flag.ErrHelp) {
			return nil, errHelpRequested
		}
		return nil, err
	}

	if showVersion {
		return nil, errVersionRequested
	}

	return &CliOptions{
		ConfigPath:  configPath,
		LogLevel:    logLevel,
		CheckConfig: checkConfig,
		Sets:        []string(sets),
	}, nil
}

// banner returns the startup banner line. It mentions the program identifier,
// build version + commit, facility id, and adapter id so an operator can match
// a running pod to its config without reading all the structured logs.
func banner(facilityID, adapterID string) string {
	return fmt.Sprintf(
		"%s starting facility=%s adapter=%s",
		versionString(), facilityID, adapterID,
	)
}

// main is the binary entry point. It parses CLI flags, loads the config,
// optionally runs --check-config, then hands off to run() with a context
// that is canceled on SIGTERM or SIGINT.
func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

// realMain is main split out so it can be unit-tested with controlled streams
// and a controlled exit code. A non-zero return becomes the process exit code.
func realMain(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	switch {
	case errors.Is(err, errHelpRequested):
		// Usage was already printed to stderr by parseFlags.
		return 0
	case errors.Is(err, errVersionRequested):
		fmt.Fprintln(stdout, versionString())
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	cfg, err := loadConfig(opts.ConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: load config:", err)
		return 1
	}
	if opts.LogLevel != "" {
		cfg.Deployment.LogLevel = opts.LogLevel
	}
	if err := applySets(cfg, opts.Sets); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "error: invalid config:", err)
		return 1
	}

	if opts.CheckConfig {
		fmt.Fprintln(stdout, "config ok")
		return 0
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if err := run(ctx, cfg, stderr); err != nil {
		fmt.Fprintln(stderr, "error: run:", err)
		return 1
	}
	return 0
}
