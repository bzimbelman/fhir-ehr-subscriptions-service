// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestCodecAAD_SwapBetweenRowsFails covers OP #109 acceptance: insert two
// encrypted ehr_events rows with different primary keys, swap their
// envelope bytes via raw SQL (simulating an operator with DB write
// access), and assert that subsequent reads error rather than silently
// returning the swapped plaintext.
func TestCodecAAD_SwapBetweenRowsFails(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	c := newCodecForTest(t)
	repo := repos.NewEhrEventsRepo(c)

	clientID := "tenant-codec-aad-swap"
	if _, err := h.DB.Exec(ctx,
		`INSERT INTO auth_clients (id) VALUES ($1) ON CONFLICT DO NOTHING`,
		clientID,
	); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	now := time.Now().UTC()
	rowA := repos.EhrEventRow{
		ID:               uuid.New(),
		ClientID:         clientID,
		TopicURL:         "http://example.org/aad-test-A",
		Focus:            "Observation/aad-A",
		ChangeKind:       repos.ChangeCreate,
		Resource:         []byte(`{"resourceType":"Observation","id":"aad-A","note":"row-A-secret"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       now,
		ResourceChangeID: uuid.New(),
	}
	rowB := repos.EhrEventRow{
		ID:               uuid.New(),
		ClientID:         clientID,
		TopicURL:         "http://example.org/aad-test-B",
		Focus:            "Observation/aad-B",
		ChangeKind:       repos.ChangeCreate,
		Resource:         []byte(`{"resourceType":"Observation","id":"aad-B","note":"row-B-secret"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       now,
		ResourceChangeID: uuid.New(),
	}

	idA, _, err := repo.Insert(ctx, h.DB, rowA)
	if err != nil {
		t.Fatalf("insert A: %v", err)
	}
	idB, _, err := repo.Insert(ctx, h.DB, rowB)
	if err != nil {
		t.Fatalf("insert B: %v", err)
	}

	// Sanity: both rows decrypt before tamper.
	mustDecryptOK(t, ctx, h, repo, idA, "row-A-secret")
	mustDecryptOK(t, ctx, h, repo, idB, "row-B-secret")

	// Capture both encrypted blobs, then swap them via raw UPDATEs.
	// This is exactly what an operator with DB write access could do.
	var encA, encB []byte
	if err := h.DB.QueryRow(ctx,
		`SELECT resource FROM ehr_events WHERE id = $1`, idA).Scan(&encA); err != nil {
		t.Fatalf("read encA: %v", err)
	}
	if err := h.DB.QueryRow(ctx,
		`SELECT resource FROM ehr_events WHERE id = $1`, idB).Scan(&encB); err != nil {
		t.Fatalf("read encB: %v", err)
	}
	if _, err := h.DB.Exec(ctx,
		`UPDATE ehr_events SET resource = $2 WHERE id = $1`, idA, encB); err != nil {
		t.Fatalf("swap A<-B: %v", err)
	}
	if _, err := h.DB.Exec(ctx,
		`UPDATE ehr_events SET resource = $2 WHERE id = $1`, idB, encA); err != nil {
		t.Fatalf("swap B<-A: %v", err)
	}

	// Both rows now hold the OTHER row's ciphertext. AAD is bound to
	// (table, id, key_version), so decrypt with row A's AAD against
	// row B's envelope MUST fail and vice versa. That is the security
	// property #109 enforces.
	if pt, err := readAndDecrypt(ctx, h, repo, idA); err == nil {
		t.Fatalf("AAD binding broken: row A decrypted under row B's ciphertext: pt=%q", string(pt))
	}
	if pt, err := readAndDecrypt(ctx, h, repo, idB); err == nil {
		t.Fatalf("AAD binding broken: row B decrypted under row A's ciphertext: pt=%q", string(pt))
	}
}

// TestCodecAAD_KeyVersionMismatchInRowFails covers the key_version
// dimension of the AAD: if an operator flips the key_version column
// without re-encrypting (e.g., to point at a different cipher), the
// AAD changes and decryption fails.
func TestCodecAAD_KeyVersionMismatchInRowFails(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	c := newCodecForTest(t)
	repo := repos.NewEhrEventsRepo(c)

	clientID := "tenant-codec-aad-kv"
	if _, err := h.DB.Exec(ctx,
		`INSERT INTO auth_clients (id) VALUES ($1) ON CONFLICT DO NOTHING`,
		clientID,
	); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	row := repos.EhrEventRow{
		ID:               uuid.New(),
		ClientID:         clientID,
		TopicURL:         "http://example.org/kv-test",
		Focus:            "Observation/kv",
		ChangeKind:       repos.ChangeCreate,
		Resource:         []byte(`{"resourceType":"Observation","id":"kv"}`),
		CorrelationID:    uuid.New(),
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	}
	id, _, err := repo.Insert(ctx, h.DB, row)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Tamper with the key_version column without re-encrypting.
	if _, err := h.DB.Exec(ctx,
		`UPDATE ehr_events SET key_version = 99 WHERE id = $1`, id); err != nil {
		t.Fatalf("tamper key_version: %v", err)
	}

	if _, err := readAndDecrypt(ctx, h, repo, id); err == nil {
		t.Fatal("expected decrypt to fail after key_version was flipped")
	}
}

// newCodecForTest mirrors the harness's codec setup with the same key
// material the harness uses, so envelopes written via the harness are
// readable here. We construct a fresh codec because no exported handle
// exists; the harness uses a fixed test key (see e2e/orchestrator/setup_test.go).
func newCodecForTest(t *testing.T) *codec.Codec {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: key}, 1)
	c, err := codec.New(kp)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	return c
}

func mustDecryptOK(t *testing.T, ctx context.Context, h *Harness, repo *repos.EhrEventsRepo, id uuid.UUID, want string) {
	t.Helper()
	got, err := readAndDecrypt(ctx, h, repo, id)
	if err != nil {
		t.Fatalf("baseline decrypt %v: %v", id, err)
	}
	if !bytesContains(got, want) {
		t.Fatalf("baseline decrypt %v: payload missing %q: got %q", id, want, string(got))
	}
}

// readAndDecrypt reads one ehr_events row by id and runs the codec
// over the row's resource bytes with AAD bound to (table, id, key_version).
// A successful return means the GCM auth tag verified.
func readAndDecrypt(ctx context.Context, h *Harness, _ *repos.EhrEventsRepo, id uuid.UUID) ([]byte, error) {
	const sql = `SELECT resource, key_version FROM ehr_events WHERE id = $1`
	var enc []byte
	var kv int32
	err := h.DB.QueryRow(ctx, sql, id).Scan(&enc, &kv)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("row not found")
		}
		return nil, err
	}
	c := newCodecNoT()
	return c.Decrypt(enc, kv, repos.AADEhrEvents(id, kv, "resource"))
}

func newCodecNoT() *codec.Codec {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	kp := codec.NewStaticKeyProvider(map[int32][]byte{1: key}, 1)
	c, _ := codec.New(kp)
	return c
}

func bytesContains(b []byte, s string) bool {
	if len(s) == 0 {
		return true
	}
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}
