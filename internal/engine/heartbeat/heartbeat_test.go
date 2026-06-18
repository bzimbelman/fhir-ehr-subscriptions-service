// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package heartbeat_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/heartbeat"
)

type fakeQuerier struct {
	mu              sync.Mutex
	candidates      []heartbeat.Candidate
	candidatesErr   error
	enqueueErr      map[uuid.UUID]error
	enqueued        []uuid.UUID
	candidatesCalls int
}

func (f *fakeQuerier) CandidatesDueForHeartbeat(_ context.Context, _ time.Time, _ int) ([]heartbeat.Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.candidatesCalls++
	if f.candidatesErr != nil {
		return nil, f.candidatesErr
	}
	out := make([]heartbeat.Candidate, len(f.candidates))
	copy(out, f.candidates)
	return out, nil
}

func (f *fakeQuerier) EnqueueHeartbeat(_ context.Context, id uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.enqueueErr[id]; ok {
		return err
	}
	f.enqueued = append(f.enqueued, id)
	return nil
}

// P2.6: TickOnce enqueues one heartbeat per due subscription.
func TestHeartbeat_TickEnqueuesEach(t *testing.T) {
	t.Parallel()
	id1, id2 := uuid.New(), uuid.New()
	q := &fakeQuerier{
		candidates: []heartbeat.Candidate{{ID: id1}, {ID: id2}},
	}
	w, err := heartbeat.New(heartbeat.Options{Querier: q})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	n, err := w.TickOnce(context.Background())
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if n != 2 {
		t.Errorf("emitted: want 2, got %d", n)
	}
	if len(q.enqueued) != 2 {
		t.Errorf("enqueued: want 2, got %d", len(q.enqueued))
	}
}

// P2.6: a per-row enqueue error is logged-and-skipped — other rows
// still get heartbeats, and the tick does not fail.
func TestHeartbeat_PerRowErrorSkippedNotPropagated(t *testing.T) {
	t.Parallel()
	id1, id2 := uuid.New(), uuid.New()
	q := &fakeQuerier{
		candidates: []heartbeat.Candidate{{ID: id1}, {ID: id2}},
		enqueueErr: map[uuid.UUID]error{id1: errors.New("transient")},
	}
	w, _ := heartbeat.New(heartbeat.Options{Querier: q})
	n, err := w.TickOnce(context.Background())
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if n != 1 {
		t.Errorf("emitted: want 1 (one error skipped), got %d", n)
	}
	if len(q.enqueued) != 1 || q.enqueued[0] != id2 {
		t.Errorf("enqueued: %v", q.enqueued)
	}
}

// P2.6: a CandidatesDueForHeartbeat error propagates so the run loop
// can backoff (real wiring bumps a metric).
func TestHeartbeat_CandidatesErrorPropagates(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{candidatesErr: errors.New("query down")}
	w, _ := heartbeat.New(heartbeat.Options{Querier: q})
	if _, err := w.TickOnce(context.Background()); err == nil {
		t.Errorf("expected error from TickOnce")
	}
}

// P2.6: New rejects missing Querier.
func TestHeartbeat_NewValidates(t *testing.T) {
	t.Parallel()
	if _, err := heartbeat.New(heartbeat.Options{}); err == nil {
		t.Errorf("expected error: missing Querier")
	}
}

// P2.6: Run drives at least one TickOnce when ctx fires before tick
// interval expires (the immediate-on-startup tick).
func TestHeartbeat_RunImmediateTick(t *testing.T) {
	t.Parallel()
	q := &fakeQuerier{candidates: []heartbeat.Candidate{{ID: uuid.New()}}}
	w, _ := heartbeat.New(heartbeat.Options{
		Querier:      q,
		TickInterval: time.Hour, // far in future, so we observe the immediate tick
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.candidatesCalls < 1 {
		t.Errorf("expected at least one TickOnce; got %d", q.candidatesCalls)
	}
	if len(q.enqueued) < 1 {
		t.Errorf("expected at least one enqueued heartbeat; got %d", len(q.enqueued))
	}
}
