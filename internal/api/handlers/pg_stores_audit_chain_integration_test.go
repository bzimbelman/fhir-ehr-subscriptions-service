//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/migrate"
)

// S-2.16 / story #49: PgAuditStore.Append must persist a real
// hash-chained row. Each row's hash = SHA-256(prev_hash || canonical_form),
// and the chain seeds from the audit module's genesis literal so the
// audit chain verifier CLI (P2.5) can validate the same chain.
func TestPgAuditStore_ChainsHashes(t *testing.T) {
	url := startPostgres(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	store := handlers.NewPgAuditStore(pool)

	cid := uuid.New()
	rows := []struct {
		action, target, outcome string
		canonical               []byte
	}{
		{"create", "Subscription/sub-1", "success", []byte(`{"k":1}`)},
		{"update", "Subscription/sub-1", "success", []byte(`{"k":2}`)},
		{"delete", "Subscription/sub-1", "success", []byte(`{"k":3}`)},
	}
	for _, r := range rows {
		if err := store.Append(ctx, r.action, r.target, r.outcome, &cid, r.canonical); err != nil {
			t.Fatalf("Append %s: %v", r.action, err)
		}
	}

	// Read back what was written, in seq order, and recompute the chain.
	type rec struct {
		actorKind, actorID, action, targetKind, targetID, outcome string
		correlationID                                             *uuid.UUID
		canonicalForm                                             []byte
		hash                                                      []byte
		prevHash                                                  []byte
	}
	const sql = `
		SELECT actor_kind, COALESCE(actor_id, ''), action,
		       COALESCE(target_kind, ''), COALESCE(target_id, ''), outcome,
		       correlation_id, canonical_form, hash, prev_hash
		FROM audit_log
		ORDER BY seq ASC`
	pgRows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer pgRows.Close()
	var got []rec
	for pgRows.Next() {
		var r rec
		var cid uuid.UUID
		if err := pgRows.Scan(&r.actorKind, &r.actorID, &r.action, &r.targetKind, &r.targetID, &r.outcome, &cid, &r.canonicalForm, &r.hash, &r.prevHash); err != nil {
			t.Fatalf("scan: %v", err)
		}
		c := cid
		r.correlationID = &c
		got = append(got, r)
	}
	if err := pgRows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("rows persisted = %d, want %d", len(got), len(rows))
	}

	// Genesis: prev_hash on the first row must equal the audit module's
	// genesis hash so the verifier seed lines up.
	genesis := audit.GenesisHashFromLiteral("")
	prev := genesis
	for i, row := range got {
		if string(row.prevHash) != string(prev) {
			t.Fatalf("row %d: prev_hash mismatch\n got=%x\nwant=%x", i, row.prevHash, prev)
		}
		// hash[N] = SHA-256(prev_hash || canonical_form). Recompute to
		// confirm the chain logic — a placeholder like []byte{0} fails
		// here loudly.
		h := sha256.New()
		h.Write(row.prevHash)
		h.Write(row.canonicalForm)
		want := h.Sum(nil)
		if string(row.hash) != string(want) {
			t.Fatalf("row %d: hash mismatch\n got=%x\nwant=%x\n canonical=%s", i, row.hash, want, row.canonicalForm)
		}
		// Each row's hash is non-degenerate.
		if len(row.hash) != sha256.Size {
			t.Fatalf("row %d: hash length = %d, want %d", i, len(row.hash), sha256.Size)
		}
		prev = row.hash
	}
}
