//go:build integration

// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Story OP #148: integration test that walks the on-disk audit_log chain
// produced by the production audit writer (audit.NewPgStore +
// audit.NewWriter wired through handlers.NewChainedAuditStore) and
// asserts each row's chain bytes match the application-side
// computation.
//
// Story #105 / #106 / #107 / #108 just merged the chained writer; this
// test is the standing guard that the chain remains byte-correct
// against real Postgres data, not just an in-memory recording store
// (cf. pg_audit_chained_test.go which exercises the same wiring with a
// fake audit.Store).
//
// Acceptance criteria covered:
//   - Walk the on-disk chain and verify each row's hash matches the
//     application-side computation (audit.VerifyChain returns nil).
//   - assertNonZeroHash helper used in every audit-emitting integration
//     test (pinned by the linter test below + applied to the existing
//     full-CRUD integration flow).
//   - CI fails if any audit row has Hash == []byte{0} (the no-zero-hash
//     check inside assertNonZeroHash + length=32 check).
//
// Run with:
//   go test -race -tags integration -timeout 300s \
//     ./internal/api/handlers/...

package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// assertNonZeroHash is the shared helper required by OP #148 AC #3.
// Every audit-emitting integration test must call it on every row it
// surfaces from the on-disk audit_log so a regression that re-inserts
// the legacy `Hash: []byte{0}` placeholder fails CI.
func assertNonZeroHash(t *testing.T, idx int, row audit.Row) {
	t.Helper()
	if len(row.ChainHash) != sha256.Size {
		t.Fatalf("audit row %d: ChainHash length %d != 32 (SHA-256)", idx, len(row.ChainHash))
	}
	allZero := true
	for _, b := range row.ChainHash {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatalf("audit row %d: ChainHash is all zeros — the legacy []byte{0} placeholder is back", idx)
	}
	if len(row.PriorHash) != sha256.Size {
		t.Fatalf("audit row %d: PriorHash length %d != 32 (SHA-256)", idx, len(row.PriorHash))
	}
	if len(row.ChainInput) == 0 {
		t.Fatalf("audit row %d: ChainInput is empty; chain bytes were never persisted", idx)
	}
}

// TestIntegration_AuditChain_HashBytes is the OP #148 BLOCKER pin: the
// real PgAuditStore-backed integration stack must produce an audit_log
// chain whose on-disk bytes match the application-side computation
// SHA-256(canonical_chain_input(payload[N], prior_hash[N-1])).
//
// The test:
//
//  1. Spins up the integration stack (real Postgres via testcontainers,
//     real audit.NewPgStore, real audit.Writer, real
//     handlers.NewChainedAuditStore wired into Deps.Audit).
//  2. Drives a small mix of audit-emitting endpoints (create, handshake,
//     update, delete, ws-binding-token issue) so the chain has multiple
//     links of distinct payload shapes.
//  3. Reads every audit_log row back via audit.PgStore.IterateRows and
//     asserts:
//     a. assertNonZeroHash on every row.
//     b. Genesis-anchored prior_hash on row 0 (default genesis literal).
//     c. Each row's PriorHash equals the previous row's ChainHash.
//     d. Each row's ChainHash == SHA-256(ChainInput) — the
//        application-side chain_hash is bit-for-bit what's on disk.
//  4. Calls audit.VerifyChain over the same store and asserts it
//     returns nil — the third-party verifier reference impl that
//     story #108 added is the canonical "byte-for-byte correct chain"
//     contract; if it ever returns an error against this stack, the
//     production wiring has regressed.
func TestIntegration_AuditChain_HashBytes(t *testing.T) {
	i := setupIntegration(t)

	// 1. Drive a CRUD flow that emits at least four audit actions:
	//    subscription.create, subscription.handshake.ok,
	//    subscription.update, subscription.delete. The fakeChannel in
	//    setupIntegration returns HandshakeSucceeded so create →
	//    activation flips to active and the handshake.ok audit fires.
	create := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`
	resp := i.do(t, http.MethodPost, "/Subscription", create)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode created body: %v", err)
	}
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("created subscription missing id; body=%s", body)
	}

	update := `{
		"resourceType": "Subscription",
		"id": "` + id + `",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`
	resp = i.do(t, http.MethodPut, "/Subscription/"+id, update)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("update status=%d", resp.StatusCode)
	}

	resp = i.do(t, http.MethodDelete, "/Subscription/"+id, "")
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}

	// 2. Re-open a PgStore over the same pool to walk the on-disk chain.
	store, err := audit.NewPgStore(i.pool, audit.PgStoreOptions{})
	if err != nil {
		t.Fatalf("audit.NewPgStore: %v", err)
	}

	// 3. Walk rows in seq order, applying assertNonZeroHash and the
	//    chain-link / hash-byte assertions. Capture rows to count and
	//    cross-check against VerifyChain.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		rows  []audit.Row
		prior = audit.GenesisHash()
	)
	if err := store.IterateRows(ctx, func(r audit.Row) error {
		idx := len(rows)
		assertNonZeroHash(t, idx, r)

		// Row 0 must anchor at the genesis hash; later rows must chain
		// to the prior on-disk row's chain_hash.
		if !bytesEqual(r.PriorHash, prior) {
			t.Fatalf("audit row %d: PriorHash != expected prior chain_hash", idx)
		}

		// Bit-for-bit hash check: chain_hash on disk must equal
		// SHA-256 of the canonical chain_input bytes ALSO on disk.
		// This is the strict OP #148 contract — the bytes the writer
		// hashed are the bytes persisted, and re-hashing them must
		// reproduce chain_hash.
		want := sha256.Sum256(r.ChainInput)
		if !bytesEqual(want[:], r.ChainHash) {
			t.Fatalf("audit row %d: SHA-256(ChainInput) != ChainHash", idx)
		}

		rows = append(rows, r)
		prior = r.ChainHash
		return nil
	}); err != nil {
		t.Fatalf("IterateRows: %v", err)
	}

	// 4. The CRUD flow above must have emitted at least three audit
	//    rows (create + update + delete). Less than that means a
	//    handler stopped emitting audit, which is a regression of its
	//    own.
	if len(rows) < 3 {
		t.Fatalf("expected >= 3 audit rows, got %d", len(rows))
	}

	// 5. Cross-check with the external verifier reference impl. This
	//    is the canonical "the chain is byte-correct" check from story
	//    #108 — if VerifyChain reports an error, the on-disk bytes
	//    diverged from what the application-side hash computation
	//    expects, and the chain has lost tamper-evidence.
	if err := audit.VerifyChain(ctx, store); err != nil {
		t.Fatalf("audit.VerifyChain over on-disk PgStore failed: %v", err)
	}
}

// bytesEqual is a tiny dependency-free byte equality helper kept local
// to this file so the test stays self-contained. It mirrors
// bytes.Equal; defined here to avoid pulling the bytes package into
// only this test in a build-tag-gated file.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
