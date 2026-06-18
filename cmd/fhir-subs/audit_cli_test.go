// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// P2.5: parseAuditVerifyFlags accepts --from and --to and parses them
// as RFC3339 timestamps.
func TestParseAuditVerifyFlags_FromTo(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	opts, err := parseAuditVerifyFlags([]string{
		"--from", "2026-06-01T00:00:00Z",
		"--to", "2026-06-30T23:59:59Z",
	}, stderr)
	if err != nil {
		t.Fatalf("parse: %v (stderr=%q)", err, stderr.String())
	}
	if opts == nil {
		t.Fatalf("opts nil")
	}
	if opts.From != time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) {
		t.Errorf("from: %v", opts.From)
	}
	if opts.To != time.Date(2026, 6, 30, 23, 59, 59, 0, time.UTC) {
		t.Errorf("to: %v", opts.To)
	}
}

// P2.5: parseAuditVerifyFlags rejects an inverted window.
func TestParseAuditVerifyFlags_InvertedWindowRejected(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	_, err := parseAuditVerifyFlags([]string{
		"--from", "2026-06-30T00:00:00Z",
		"--to", "2026-06-01T00:00:00Z",
	}, stderr)
	if err == nil {
		t.Fatalf("expected error on inverted window")
	}
	if !strings.Contains(err.Error(), "before --from") {
		t.Errorf("wrong error: %v", err)
	}
}

// P2.5: parseAuditVerifyFlags rejects malformed timestamps.
func TestParseAuditVerifyFlags_MalformedFromRejected(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	_, err := parseAuditVerifyFlags([]string{"--from", "not-a-time"}, stderr)
	if err == nil {
		t.Fatalf("expected error on malformed --from")
	}
}

// P2.5: parseAuditVerifyFlags surfaces --help via errHelpRequested so
// realMain can exit 0.
func TestParseAuditVerifyFlags_HelpRequested(t *testing.T) {
	t.Parallel()
	stderr := &bytes.Buffer{}
	_, err := parseAuditVerifyFlags([]string{"-h"}, stderr)
	if !errors.Is(err, errHelpRequested) {
		t.Fatalf("expected errHelpRequested, got %v", err)
	}
}

// P2.5: realMain's "audit" first-arg dispatch routes to the audit
// subcommand handler. Unknown verbs return exit 2 with a helpful
// stderr line that names the expected verb.
func TestRealMain_AuditUnknownVerb(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"audit", "frobnicate"}, stdout, stderr)
	if rc != 2 {
		t.Errorf("rc: want 2, got %d (stderr=%q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), `unknown audit verb "frobnicate"`) {
		t.Errorf("stderr missing diagnostic: %q", stderr.String())
	}
}

// P2.5: `audit` with no further args prints usage to stderr and exits 2
// rather than trying to parse the empty arglist as `audit verify`.
func TestRealMain_AuditMissingVerb(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"audit"}, stdout, stderr)
	if rc != 2 {
		t.Errorf("rc: want 2, got %d (stderr=%q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage:") {
		t.Errorf("stderr missing usage: %q", stderr.String())
	}
}
