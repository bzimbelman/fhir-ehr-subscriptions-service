// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// TestB34_AuditWriterPanicReleasesChainLock (B-34) verifies that a
// panic in the durable insert path does not leak the chain advisory
// lock. Pre-fix: a panic between AcquireChainLock and the manual
// release stranded the lock forever; subsequent Emit calls blocked
// indefinitely.
//
// We exercise the contract through the same Store interface that the
// production pgstore satisfies. The fault-injection seam is a fake
// store that panics on the next InsertAuditRow when armed. The
// post-fix Writer recovers, releases the lock, and surfaces the panic
// as an error — so the next Emit completes promptly.
func TestB34_AuditWriterPanicReleasesChainLock(t *testing.T) {
	store := newAuditPanicStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	store.armPanic()

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic propagated past Writer.Emit: %v", r)
			}
		}()
		err := w.Emit(context.Background(), audit.Event{
			ActorKind: "system", Action: "x", Outcome: "success",
		})
		if err == nil {
			t.Fatalf("expected error from panicking insert; got nil")
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- w.Emit(context.Background(), audit.Event{
			ActorKind: "system", Action: "x", Outcome: "success",
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("post-panic Emit should succeed; got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("post-panic Emit blocked — chain advisory lock leaked")
	}
}

// auditPanicStore is a fake Store that lets the test arm a panic in
// the next InsertAuditRow call. The chain mutex models the
// pgstore's advisory lock.
type auditPanicStore struct {
	chain sync.Mutex
	mu    sync.Mutex
	rows  []*audit.Row
	armed bool
}

func newAuditPanicStore() *auditPanicStore { return &auditPanicStore{} }

func (s *auditPanicStore) armPanic() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *auditPanicStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.mu.Lock()
	armed := s.armed
	s.armed = false
	s.mu.Unlock()
	if armed {
		panic(errors.New("simulated insert panic"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := row
	s.rows = append(s.rows, &r)
	return nil
}

func (s *auditPanicStore) LastChainHash(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *auditPanicStore) AcquireChainLock(_ context.Context) (func() error, error) {
	s.chain.Lock()
	return func() error {
		s.chain.Unlock()
		return nil
	}, nil
}

func (s *auditPanicStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
	s.mu.Lock()
	rows := append([]*audit.Row(nil), s.rows...)
	s.mu.Unlock()
	for _, r := range rows {
		if err := fn(*r); err != nil {
			return err
		}
	}
	return nil
}
