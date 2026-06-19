// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package migrate_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// TestAuditLogColumnsAlignWithCode asserts that the embedded migrations
// produce an audit_log table whose columns match the names that
// internal/infra/observability/audit/pgstore.go reads/writes:
// chain_hash, prior_hash, chain_input, payload. Story #106 covers the
// schema-mismatch finding where the original migration creates "hash",
// "prev_hash", "canonical_form" (and no "payload"), which makes
// `fhir-subs audit verify` fail immediately on a real DB.
func TestAuditLogColumnsAlignWithCode(t *testing.T) {
	t.Parallel()

	migs, err := migrate.Embedded()
	if err != nil {
		t.Fatalf("Embedded(): %v", err)
	}

	combined := strings.Builder{}
	for _, m := range migs {
		combined.WriteString(m.Body)
		combined.WriteByte('\n')
	}
	body := combined.String()

	required := []string{"chain_hash", "prior_hash", "chain_input", "payload"}
	for _, col := range required {
		if !strings.Contains(body, col) {
			t.Errorf("expected migrations to define column %q on audit_log; not found in any embedded migration", col)
		}
	}

	// The legacy column names must NOT remain (otherwise both names
	// coexist and the rename was incomplete).
	legacy := []string{"canonical_form"}
	for _, col := range legacy {
		// Allow it to appear inside a "rename to" statement (we want
		// a numbered migration that drops/renames the legacy name)
		// but it must not be the FINAL declaration. Cheap heuristic:
		// a final-state migration should not have a `bytea not null`
		// declaration on the legacy column name.
		needle := col + " bytea"
		if strings.Contains(strings.ToLower(body), needle) {
			// Allow only if we also see the rename. The intent is the
			// legacy column has been renamed/dropped.
			if !strings.Contains(strings.ToLower(body), "rename column "+col) &&
				!strings.Contains(strings.ToLower(body), "rename "+col) &&
				!strings.Contains(strings.ToLower(body), "drop column "+col) {
				t.Errorf("legacy column %q still declared and never renamed/dropped", col)
			}
		}
	}
}
