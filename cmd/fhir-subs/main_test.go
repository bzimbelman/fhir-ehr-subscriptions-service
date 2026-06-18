// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestParseFlags_Help asserts that --help returns the help-requested sentinel
// so main can print usage and exit 0.
func TestParseFlags_Help(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	opts, err := parseFlags([]string{"--help"}, &out)
	if err != errHelpRequested {
		t.Fatalf("expected errHelpRequested, got %v", err)
	}
	if opts != nil {
		// A nil opts is fine; we only care that the sentinel comes back.
		_ = opts
	}
	if !strings.Contains(out.String(), "Usage of fhir-subs") {
		t.Fatalf("usage not printed: %q", out.String())
	}
	if !strings.Contains(out.String(), "--config") {
		t.Fatalf("--config flag not in usage: %q", out.String())
	}
}

// TestParseFlags_Version asserts that --version returns the version-requested sentinel.
func TestParseFlags_Version(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	_, err := parseFlags([]string{"--version"}, &out)
	if err != errVersionRequested {
		t.Fatalf("expected errVersionRequested, got %v", err)
	}
}

// TestVersionString asserts the formatted version output.
func TestVersionString(t *testing.T) {
	t.Parallel()

	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version, Commit = prevV, prevC })

	Version = "1.2.3"
	Commit = "abc1234"

	got := versionString()
	if !strings.Contains(got, "fhir-subs") {
		t.Fatalf("missing program name in %q", got)
	}
	if !strings.Contains(got, "1.2.3") {
		t.Fatalf("missing version in %q", got)
	}
	if !strings.Contains(got, "abc1234") {
		t.Fatalf("missing commit in %q", got)
	}
}

// TestParseFlags_Defaults asserts default values for absent flags.
func TestParseFlags_Defaults(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	opts, err := parseFlags([]string{}, &out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if opts.ConfigPath != "/etc/fhir-subs/config.yaml" {
		t.Fatalf("unexpected default config path: %q", opts.ConfigPath)
	}
	if opts.LogLevel != "" {
		t.Fatalf("expected empty log level default (so config wins), got %q", opts.LogLevel)
	}
	if opts.CheckConfig {
		t.Fatalf("expected --check-config default false")
	}
	if len(opts.Sets) != 0 {
		t.Fatalf("expected no --set entries, got %v", opts.Sets)
	}
}

// TestParseFlags_AllFlags asserts that every documented flag is parsed.
func TestParseFlags_AllFlags(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	args := []string{
		"--config", "/tmp/x.yaml",
		"--log-level", "debug",
		"--check-config",
		"--set", "a.b=1",
		"--set", "c.d=2",
	}
	opts, err := parseFlags(args, &out)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if opts.ConfigPath != "/tmp/x.yaml" {
		t.Fatalf("config: %q", opts.ConfigPath)
	}
	if opts.LogLevel != "debug" {
		t.Fatalf("log-level: %q", opts.LogLevel)
	}
	if !opts.CheckConfig {
		t.Fatalf("check-config not set")
	}
	if len(opts.Sets) != 2 || opts.Sets[0] != "a.b=1" || opts.Sets[1] != "c.d=2" {
		t.Fatalf("sets: %v", opts.Sets)
	}
}

// TestBannerContains asserts the startup banner mentions the load-bearing identifiers.
func TestBannerContains(t *testing.T) {
	t.Parallel()

	prevV, prevC := Version, Commit
	t.Cleanup(func() { Version, Commit = prevV, prevC })
	Version = "9.9.9"
	Commit = "deadbee"

	b := banner("facility-x", "adapter-y")
	for _, want := range []string{"fhir-subs", "9.9.9", "deadbee", "facility-x", "adapter-y"} {
		if !strings.Contains(b, want) {
			t.Fatalf("banner missing %q: %q", want, b)
		}
	}
}
