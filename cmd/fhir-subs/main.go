// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Command fhir-subs is the entry point for the fhir-subscriptions-foss server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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

// main is wired in later TDD commits. For now it is a stub so go build passes
// once the supporting tests/files arrive; commit 8 of this branch replaces it
// with the real run loop.
func main() {
	// Placeholder; real wiring lands later in this branch.
	fmt.Fprintln(os.Stderr, "fhir-subs: main not yet wired")
	os.Exit(2)
}
