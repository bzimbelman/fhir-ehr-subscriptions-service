// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"os"
	"strings"
	"testing"
)

// TestPgAuditStore_NoZeroByteHashPlaceholder guards story #105 finding #9:
// pg_stores.go must not write the literal `Hash: []byte{0}` placeholder.
// Production audit chain integrity is impossible while the placeholder
// is in place. Phase B replaces it with the chained writer (see
// pg_audit_chained_test.go).
func TestPgAuditStore_NoZeroByteHashPlaceholder(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("pg_stores.go")
	if err != nil {
		t.Fatalf("read pg_stores.go: %v", err)
	}
	src := string(body)
	if strings.Contains(src, "Hash:          []byte{0}") || strings.Contains(src, "Hash: []byte{0}") {
		t.Errorf("pg_stores.go still writes the audit Hash: []byte{0} placeholder; story #105 requires the hash-chained writer")
	}
}

// TestWiringUsesChainedAuditStore guards story #105 acceptance criterion #1:
// the production wiring at cmd/fhir-subs/wiring.go MUST not pass the
// PgAuditStore placeholder into Deps.Audit; it must use the chained
// writer (handlers.NewChainedAuditStore wrapping the audit.Writer
// returned by observability.Start).
func TestWiringUsesChainedAuditStore(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("../../../cmd/fhir-subs/wiring.go")
	if err != nil {
		t.Fatalf("read wiring.go: %v", err)
	}
	src := string(body)
	if strings.Contains(src, "Audit:               handlers.NewPgAuditStore(pool)") ||
		strings.Contains(src, "Audit: handlers.NewPgAuditStore(pool)") {
		t.Errorf("wiring.go still wires the placeholder PgAuditStore; story #105 requires the chained writer")
	}
}
