// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"os"
	"strings"
	"testing"
)

// TestPgStore_DoesNotSubstituteCorrelationIDOrTimestamp guards story #107:
// the pg-backed audit store MUST persist the application-supplied
// CorrelationID and OccurredAt verbatim. Substituting `uuid.New()` (or
// relying on server-side `now()`) would diverge the on-disk row from
// the bytes the writer hashed into chain_input, making external
// verifiers structurally unable to validate the chain.
//
// This is a deliberately-narrow source-level guard: we read the package
// source for two specific anti-patterns. Behavioral coverage of the
// resulting on-disk shape is provided by the audit_integration_test
// (real Postgres) and by the canonical-input recompute test in
// audit_chain_fixes_test.go.
func TestPgStore_DoesNotSubstituteCorrelationIDOrTimestamp(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("pgstore.go")
	if err != nil {
		t.Fatalf("read pgstore.go: %v", err)
	}
	src := string(body)

	bad := []struct {
		needle string
		why    string
	}{
		{
			needle: "uuid.New()",
			why:    "pgstore must not substitute a fresh UUID for a zero CorrelationID; the chain_input was hashed over the application-supplied CID and a substitution would diverge",
		},
		{
			needle: "now()",
			why:    "pgstore must not let server-side now() populate occurred_at; the application-supplied OccurredAt was hashed and the on-disk timestamp must match it byte-for-byte",
		},
		{
			needle: "DEFAULT now()",
			why:    "the audit_log migration must not default occurred_at to server-side now() when the application is responsible for the timestamp it hashed",
		},
	}
	for _, b := range bad {
		if strings.Contains(src, b.needle) {
			t.Errorf("pgstore.go contains %q which is forbidden: %s", b.needle, b.why)
		}
	}
}
