// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_AuditChainIsValid covers the audit_log emitter + chain
// verifier: every audit row's hash must equal SHA256(prev_hash ||
// canonical_payload), forming an unbroken chain. The test exercises a
// real subscription create against the binary, then walks the
// audit_log table on the real Postgres and verifies the chain.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_AuditChainIsValid(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	for i := 0; i < 3; i++ {
		_ = s.postSubscription(restHookSubscriptionJSON(s.stack,
			"http://example.org/topics/service-request-scan-changed", tag))
	}

	// Poll the audit_log table for at least 3 rows.
	deadline := time.Now().Add(30 * time.Second)
	var rows []auditRow
	for time.Now().Before(deadline) {
		rows = readAuditRows(t, s.ctx, s.stack.Postgres.URL)
		if len(rows) >= 3 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if len(rows) < 3 {
		t.Fatalf("AuditChainIsValid: expected >=3 audit_log rows after 3 subscription creates; got %d", len(rows))
	}

	// Walk and verify the hash chain.
	var prev []byte
	for i, r := range rows {
		want := sha256.Sum256(append(append([]byte{}, prev...), r.canonicalPayload...))
		if hex.EncodeToString(want[:]) != r.hashHex {
			t.Fatalf("audit_log row %d (id=%d): hash mismatch — chain broken at this row", i, r.id)
		}
		prev = want[:]
	}
}

type auditRow struct {
	id               int64
	canonicalPayload []byte
	hashHex          string
}

func readAuditRows(t *testing.T, ctx context.Context, dbURL string) []auditRow {
	t.Helper()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open pg: %v", err)
	}
	defer db.Close()
	q := `SELECT id, canonical_payload, encode(row_hash, 'hex') FROM audit_log ORDER BY id ASC`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.id, &r.canonicalPayload, &r.hashHex); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	return out
}
