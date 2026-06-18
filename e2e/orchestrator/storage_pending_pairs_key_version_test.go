// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// TestE2E_PendingPairs_DecryptsWithRowKeyVersion pins B-21/B-22.
//
// Scenario: a pending_pairs row is written under key_version=1. Then
// the operator rotates keys — version 2 is now active, version 1 is
// retained. The held half is decrypted later (e.g., after a process
// restart). The Decrypt path MUST use the row's persisted key_version
// (=1), not the codec's currently-active version. Pre-fix the
// hardcoded `1` happened to work, but the row had no column to read
// the version from in the first place — so a real rotation could not
// be implemented.
func TestE2E_PendingPairs_DecryptsWithRowKeyVersion(t *testing.T) {
	t.Parallel()
	h := requireHarness(t)
	ctx := context.Background()

	plaintext := []byte(`{"resourceType":"ServiceRequest","id":"sr-key-version"}`)

	// Codec A: only knows key version 1.
	cdA, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{
		1: harnessCodecKey(),
	}, 1))
	if err != nil {
		t.Fatalf("codec A: %v", err)
	}
	repoA := repos.NewPendingPairsRepo(cdA)

	// Pre-flight: insert a hl7_message_queue row to satisfy the FK on
	// pending_pairs.source_message_id.
	srcID, err := h.insertHL7Source(ctx)
	if err != nil {
		t.Fatalf("insert hl7 source: %v", err)
	}

	corrKey := "ORD-key-rotation-" + uuid.NewString()
	endpoint := "adt-feed"
	if insErr := repoA.Insert(ctx, h.DB, repos.PendingPairRow{
		CorrelationKey:   corrKey,
		ListenerEndpoint: endpoint,
		PendingResource:  plaintext,
		PendingKind:      repos.PendingDelete,
		SourceMessageID:  srcID,
		ExpiresAt:        time.Now().Add(time.Hour),
	}); insErr != nil {
		t.Fatalf("insert pending pair: %v", insErr)
	}

	// Validate row carries key_version=1.
	var kv int32
	if scanErr := h.DB.QueryRow(ctx,
		`SELECT key_version FROM pending_pairs WHERE correlation_key=$1 AND listener_endpoint=$2`,
		corrKey, endpoint,
	).Scan(&kv); scanErr != nil {
		t.Fatalf("read key_version: %v", scanErr)
	}
	if kv != 1 {
		t.Fatalf("expected key_version=1, got %d", kv)
	}

	// Codec B: rotated. Version 2 is active, version 1 still loaded for
	// reads. Decrypt MUST use the row's stored key_version.
	keyV2 := make([]byte, 32)
	for i := range keyV2 {
		keyV2[i] = byte(i*2 + 7)
	}
	cdB, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{
		1: harnessCodecKey(),
		2: keyV2,
	}, 2))
	if err != nil {
		t.Fatalf("codec B: %v", err)
	}
	repoB := repos.NewPendingPairsRepo(cdB)

	// ClaimExpired forces expires_at <= now; we stamped 1h in the
	// future so flip the row to be expired before claiming.
	if _, flipErr := h.DB.Exec(ctx,
		`UPDATE pending_pairs SET expires_at = now() - interval '1 second'
		   WHERE correlation_key=$1 AND listener_endpoint=$2`,
		corrKey, endpoint,
	); flipErr != nil {
		t.Fatalf("flip expires_at: %v", flipErr)
	}

	tx, err := h.DB.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := repoB.ClaimExpired(ctx, tx, 16, time.Now())
	if err != nil {
		t.Fatalf("claim with rotated codec: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 claimed row, got %d", len(rows))
	}
	if string(rows[0].PendingResource) != string(plaintext) {
		t.Fatalf("decrypted plaintext mismatch:\ngot  %q\nwant %q",
			rows[0].PendingResource, plaintext)
	}
	if rows[0].KeyVersion != 1 {
		t.Errorf("row.KeyVersion = %d, want 1", rows[0].KeyVersion)
	}
}

// insertHL7Source writes a minimal hl7_message_queue row and returns
// its id. pending_pairs.source_message_id has a FK to that table.
func (h *Harness) insertHL7Source(ctx context.Context) (uuid.UUID, error) {
	id := uuid.New()
	corr := uuid.New()
	_, err := h.DB.Exec(ctx, `
		INSERT INTO hl7_message_queue
			(id, listener_endpoint, peer_addr, mllp_message_id, correlation_id, raw_body, key_version)
		VALUES ($1, 'adt-feed', '10.0.0.1:5000', $2, $3, $4, 1)`,
		id, "MSG-"+uuid.NewString(), corr, []byte("MSH|test"))
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}
