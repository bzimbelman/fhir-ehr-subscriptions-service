// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package audit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

func timeoutAfterShort() <-chan time.Time {
	return time.After(2 * time.Second)
}

// B-34: a panic in the durable insert path MUST NOT leave the chain
// advisory lock held. The contract on Writer.Emit is: on any path out
// (success, error, or panic) the lock is released and subsequent Emit
// calls succeed. Without `defer recover()` over the lock holder, a
// panic mid-Insert leaks the advisory lock forever.
func TestEmit_PanicDuringInsertReleasesLock(t *testing.T) {
	t.Parallel()
	store := newPanickingStore()
	w, err := audit.NewWriter(audit.WriterOptions{
		Store: store,
		Sink:  audit.NewStdoutSink(),
	})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}

	// Arm the panic in the next Insert.
	store.armInsertPanic()

	// First Emit panics inside InsertAuditRow. The Writer must catch
	// the panic, release the lock, and surface an error.
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

	// Lock must be released — a fresh Emit must complete promptly. If
	// the lock leaked, the next AcquireChainLock blocks forever.
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
	case <-timeoutAfterShort():
		t.Fatalf("post-panic Emit blocked — advisory lock leaked")
	}
}

// panickingStore mirrors fakeStore but lets the test arm a panic in the
// next InsertAuditRow call. It exercises the same lock semantics
// (chain serializes through chain.Mutex).
type panickingStore struct {
	chain       sync.Mutex
	mu          sync.Mutex
	rows        []*audit.Row
	insertPanic bool
}

func newPanickingStore() *panickingStore { return &panickingStore{} }

func (s *panickingStore) armInsertPanic() {
	s.mu.Lock()
	s.insertPanic = true
	s.mu.Unlock()
}

func (s *panickingStore) InsertAuditRow(_ context.Context, row audit.Row) error {
	s.mu.Lock()
	armed := s.insertPanic
	s.insertPanic = false
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

func (s *panickingStore) LastChainHash(_ context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return nil, nil
	}
	return s.rows[len(s.rows)-1].ChainHash, nil
}

func (s *panickingStore) AcquireChainLock(_ context.Context) (func() error, error) {
	s.chain.Lock()
	return func() error {
		s.chain.Unlock()
		return nil
	}, nil
}

func (s *panickingStore) IterateRows(_ context.Context, fn func(audit.Row) error) error {
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
