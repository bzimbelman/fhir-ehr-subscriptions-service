// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestNewChainedAuditStore_WritesNonZeroChainHashes pins story #105:
// the AuditStore wired into production must produce real hash-chained
// rows, NOT the placeholder Hash: []byte{0}. Two appends must yield two
// distinct, non-zero, correctly-chained chain_hash bytes.
//
// The wiring layer is expected to expose handlers.NewChainedAuditStore
// (a small adapter that wraps audit.Writer + audit.Store and satisfies
// the handlers.AuditStore contract). Until that exists, this test
// fails to compile, which is the RED signal Phase B must address.
func TestNewChainedAuditStore_WritesNonZeroChainHashes(t *testing.T) {
	t.Parallel()
	store := newRecordingAuditStore()
	w, err := audit.NewWriter(audit.WriterOptions{Store: store})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	adapter := handlers.NewChainedAuditStore(w)

	id1 := uuid.New()
	if err := adapter.Append(context.Background(), "subscription.create", "sub-1", "success", &id1, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	id2 := uuid.New()
	if err := adapter.Append(context.Background(), "subscription.update", "sub-1", "success", &id2, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	rows := store.snapshot()
	if len(rows) != 2 {
		t.Fatalf("rows: got %d want 2", len(rows))
	}
	for i, r := range rows {
		// Reject the legacy placeholder shape (one zero byte) and any
		// all-zero hash; chain_hash must be a real SHA-256 digest.
		if len(r.ChainHash) != sha256.Size {
			t.Fatalf("row %d: ChainHash length %d != 32 (SHA-256)", i, len(r.ChainHash))
		}
		allZero := true
		for _, b := range r.ChainHash {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			t.Fatalf("row %d: ChainHash is all zeros — placeholder still in place", i)
		}
	}

	// The two hashes must differ and chain together.
	if bytes.Equal(rows[0].ChainHash, rows[1].ChainHash) {
		t.Fatalf("two appends produced identical chain hashes")
	}
	if !bytes.Equal(rows[1].PriorHash, rows[0].ChainHash) {
		t.Fatalf("row 1 prior_hash != row 0 chain_hash; chain not linked")
	}
}

// recordingAuditStore is a minimal in-memory audit.Store used to
// observe what audit.Writer produced when Append() is invoked through
// the handlers.AuditStore adapter.
type recordingAuditStore struct {
	chain sync.Mutex
	mu    sync.Mutex
	rows  []audit.Row
}

func newRecordingAuditStore() *recordingAuditStore { return &recordingAuditStore{} }

func (s *recordingAuditStore) InsertAuditRow(_ context.Context, r audit.Row) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, r)
	return nil
}

func (s *recordingAuditStore) LastChainHash(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *recordingAuditStore) AcquireChainLock(_ context.Context) (func() error, error) {
	s.chain.Lock()
	return func() error {
		s.chain.Unlock()
		return nil
	}, nil
}

func (s *recordingAuditStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	s.mu.Lock()
	rows := append([]audit.Row(nil), s.rows...)
	s.mu.Unlock()
	for _, r := range rows {
		if err := fn(r); err != nil {
			return err
		}
	}
	return nil
}

func (s *recordingAuditStore) snapshot() []audit.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Row, len(s.rows))
	copy(out, s.rows)
	return out
}
