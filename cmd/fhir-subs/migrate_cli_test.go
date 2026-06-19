// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"
)

// OP #212: realMain dispatches the `migrate` first positional argument
// to the migrate subcommand handler, mirroring the existing audit
// dispatch. Unknown verbs print usage and exit 2.

// TestRealMain_MigrateUnknownVerb asserts that an unknown migrate verb
// prints a usage line that names the supported verbs and exits 2. The
// exit code matches the binary's flag-parsing convention.
func TestRealMain_MigrateUnknownVerb(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"migrate", "frobnicate"}, stdout, stderr)
	if rc != 2 {
		t.Errorf("rc: want 2, got %d (stderr=%q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), `unknown migrate verb "frobnicate"`) {
		t.Errorf("stderr missing diagnostic: %q", stderr.String())
	}
}

// TestRealMain_MigrateMissingVerb asserts that `migrate` with no further
// args prints usage to stderr and exits 2.
func TestRealMain_MigrateMissingVerb(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"migrate"}, stdout, stderr)
	if rc != 2 {
		t.Errorf("rc: want 2, got %d (stderr=%q)", rc, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(strings.ToLower(out), "usage:") {
		t.Errorf("stderr missing usage prefix: %q", out)
	}
	// All three subcommands MUST be discoverable from the usage line so
	// an operator who runs the bare verb learns what's available.
	for _, want := range []string{"up", "down", "status"} {
		if !strings.Contains(out, want) {
			t.Errorf("usage line missing %q verb: %q", want, out)
		}
	}
}

// TestRealMain_MigrateDownIsRejected pins the documented out-of-scope
// boundary in #212: `migrate down` semantics are intentionally not
// implemented. The verb MUST be recognized (so it is discoverable) but
// MUST exit non-zero with a clear "not supported" message rather than
// silently no-op or attempt a destructive teardown.
func TestRealMain_MigrateDownIsRejected(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"migrate", "down"}, stdout, stderr)
	if rc == 0 {
		t.Errorf("migrate down must not exit 0 (stderr=%q)", stderr.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "not supported") {
		t.Errorf("stderr should explain that 'migrate down' is not supported: %q", stderr.String())
	}
}

// TestParseMigrateFlags_HelpRequested asserts the migrate subcommand's
// flag parser surfaces --help via errHelpRequested so realMain can exit 0
// (matches the audit subcommand contract).
func TestParseMigrateFlags_HelpRequested(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	_, err := parseMigrateFlags([]string{"-h"}, stderr)
	if err != errHelpRequested {
		t.Fatalf("expected errHelpRequested, got %v", err)
	}
}

// TestParseMigrateFlags_DefaultsConfigPath asserts the migrate flag
// parser picks up the canonical config-file location when --config is
// omitted, matching the rest of the binary.
func TestParseMigrateFlags_DefaultsConfigPath(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	opts, err := parseMigrateFlags([]string{}, stderr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if opts.ConfigPath != defaultConfigPath {
		t.Errorf("config path: %q, want %q", opts.ConfigPath, defaultConfigPath)
	}
}
