//go:build integration

// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the scheduler worker. Same skip-on-no-docker
// shape as the matcher and submatcher tests.

package scheduler_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/engine/builder"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/engine/scheduler"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	type runResult struct {
		url string
		err error
	}
	res := make(chan runResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				res <- runResult{err: fmt.Errorf("docker not available: %v", r)}
			}
		}()
		container, err := tcpostgres.Run(ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("scheduler_test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx/v5"),
		)
		if err != nil {
			res <- runResult{err: err}
			return
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		url, err := container.ConnectionString(ctx, "sslmode=disable")
		if err != nil {
			res <- runResult{err: err}
			return
		}
		res <- runResult{url: url}
	}()
	r := <-res
	if r.err != nil {
		t.Skipf("postgres container unavailable; skipping integration test: %v", r.err)
	}
	return r.url
}

func newTestStorage(t *testing.T, url string) *storage.Storage {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cfg := storage.Config{
		PostgresURL: url,
		KeyVersions: map[int32][]byte{1: key},
		ActiveKey:   1,
	}
	cfg.Partitioning.AutoDrop = false
	cfg.Partitioning.RunInterval = time.Hour
	cfg.Retention.RunInterval = time.Hour
	cfg.Retention.Hl7MessageQueue = 0

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := storage.Start(ctx, cfg, storage.Context{})
	if err != nil {
		t.Fatalf("storage.Start: %v", err)
	}
	t.Cleanup(func() {
		shctx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		defer sc()
		_ = s.Shutdown(shctx)
	})
	return s
}

// fakeChannel is a configurable Channel implementation. Tests script
// the outcome sequence; each Deliver call returns the next outcome,
// and Calls captures the deliveries dispatched.
type fakeChannel struct {
	mu       sync.Mutex
	outcomes []channel.DeliveryOutcome
	idx      int
	calls    []channel.NotificationEnvelope
	wg       *sync.WaitGroup
}

func (f *fakeChannel) Deliver(_ context.Context, env channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, env)
	if f.wg != nil {
		f.wg.Done()
	}
	if f.idx >= len(f.outcomes) {
		return channel.Delivered(), nil
	}
	out := f.outcomes[f.idx]
	f.idx++
	return out, nil
}

func (f *fakeChannel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// seedAuthClient mirrors the helper in submatcher tests.
func seedAuthClient(t *testing.T, s *storage.Storage, id string) {
	t.Helper()
	if _, err := s.Pool().Pgx().Exec(context.Background(),
		`INSERT INTO auth_clients(id) VALUES ($1) ON CONFLICT DO NOTHING`, id); err != nil {
		t.Fatalf("seed auth_client: %v", err)
	}
}

// seedFanout creates one subscription, one ehr_events row, and one
// pending deliveries row keyed (subscription_id, event_number=1).
// Returns subID + deliveryID + ehrEventID.
func seedFanout(t *testing.T, s *storage.Storage, channelType string) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	seedAuthClient(t, s, "client-S")
	subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
		ClientID:    "client-S",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/t",
		ChannelType: channelType,
		Endpoint:    "https://sub.example.org/notif",
		Content:     "id-only",
	})
	if err != nil {
		t.Fatalf("insert sub: %v", err)
	}

	corr := uuid.New()
	ehrID, _, err := s.EhrEvents().Insert(ctx, s.Pool().Pgx(), repos.EhrEventRow{
		TopicURL:         "http://example.org/t",
		Focus:            "ServiceRequest/abc",
		ChangeKind:       repos.ChangeCreate,
		Resource:         []byte(`{"resourceType":"ServiceRequest","id":"abc","status":"active"}`),
		CorrelationID:    corr,
		OccurredAt:       time.Now().UTC(),
		ResourceChangeID: uuid.New(),
	})
	if err != nil {
		t.Fatalf("insert ehr_events: %v", err)
	}

	delID, err := s.Deliveries().Insert(ctx, s.Pool().Pgx(), repos.DeliveryRow{
		SubscriptionID: subID,
		EhrEventID:     ehrID,
		EventNumber:    1,
		Status:         repos.DeliveryPending,
		Attempts:       0,
		NextAttemptAt:  time.Now().UTC().Add(-time.Second),
		CorrelationID:  corr,
	})
	if err != nil {
		t.Fatalf("insert deliveries: %v", err)
	}
	return subID, delID, ehrID
}

func newScheduler(t *testing.T, s *storage.Storage, registry scheduler.ChannelRegistry, retry scheduler.RetryConfig) *scheduler.Worker {
	t.Helper()
	bldr := builder.New(builder.Config{})
	cfg := scheduler.Config{ClaimBatchSize: 16, Retry: retry}
	return scheduler.NewWorker(
		s.Pool().Pgx(),
		s.Subscriptions(), s.EhrEvents(), s.Deliveries(), s.DeadLetters(),
		registry, bldr, cfg,
		scheduler.Options{RNG: scheduler.DeterministicRNG(1)},
	)
}

// TestIntegrationSchedulerHappyPath: a Delivered outcome marks the row
// delivered and advances the subscription cursor.
func TestIntegrationSchedulerHappyPath(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	subID, delID, _ := seedFanout(t, s, "rest-hook-fake")
	reg := scheduler.NewMapRegistry()
	fake := &fakeChannel{outcomes: []channel.DeliveryOutcome{channel.Delivered()}}
	reg.Register("rest-hook-fake", fake)

	w := newScheduler(t, s, reg, scheduler.RetryConfig{MaxAttempts: 5})
	processed, err := w.TickOnce(ctx)
	if err != nil {
		t.Fatalf("TickOnce: %v", err)
	}
	if !processed {
		t.Fatal("expected processed=true")
	}

	if fake.callCount() != 1 {
		t.Fatalf("channel.Deliver call count: got %d want 1", fake.callCount())
	}

	dlv, err := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if err != nil || dlv == nil {
		t.Fatalf("get delivery: %v", err)
	}
	if dlv.Status != repos.DeliveryDelivered {
		t.Errorf("status: got %q want delivered", dlv.Status)
	}
	if dlv.Attempts != 1 {
		t.Errorf("attempts: got %d want 1", dlv.Attempts)
	}

	sub, _ := s.Subscriptions().GetByID(ctx, s.Pool().Pgx(), subID)
	if sub.EventsSinceSubscriptionStart != 1 {
		t.Errorf("subscription cursor: got %d want 1", sub.EventsSinceSubscriptionStart)
	}
}

// TestIntegrationSchedulerTransientReschedules: a Transient outcome
// flips the row back to pending with attempts++ and a future
// next_attempt_at.
func TestIntegrationSchedulerTransientReschedules(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	_, delID, _ := seedFanout(t, s, "rest-hook-fake")
	reg := scheduler.NewMapRegistry()
	fake := &fakeChannel{outcomes: []channel.DeliveryOutcome{
		channel.TransientFailure(0, "5xx"),
	}}
	reg.Register("rest-hook-fake", fake)

	t0 := time.Now().UTC()
	w := newScheduler(t, s, reg, scheduler.RetryConfig{
		Initial: 5 * time.Second, Max: time.Minute, Min: time.Second,
		MaxAttempts: 5,
	})
	if _, err := w.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	dlv, err := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if err != nil || dlv == nil {
		t.Fatalf("get delivery: %v", err)
	}
	if dlv.Status != repos.DeliveryPending {
		t.Errorf("status: got %q want pending", dlv.Status)
	}
	if dlv.Attempts != 1 {
		t.Errorf("attempts: got %d want 1", dlv.Attempts)
	}
	if !dlv.NextAttemptAt.After(t0) {
		t.Errorf("next_attempt_at not advanced: got %v", dlv.NextAttemptAt)
	}
	if dlv.LastError != "5xx" {
		t.Errorf("last_error: got %q want %q", dlv.LastError, "5xx")
	}
}

// TestIntegrationSchedulerPermanentDeadLetters: a Permanent outcome
// moves the row to 'dead' and writes a dead_letters row.
func TestIntegrationSchedulerPermanentDeadLetters(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	subID, delID, _ := seedFanout(t, s, "rest-hook-fake")
	reg := scheduler.NewMapRegistry()
	fake := &fakeChannel{outcomes: []channel.DeliveryOutcome{
		channel.PermanentFailure("404 not found"),
	}}
	reg.Register("rest-hook-fake", fake)

	w := newScheduler(t, s, reg, scheduler.RetryConfig{MaxAttempts: 5})
	if _, err := w.TickOnce(ctx); err != nil {
		t.Fatalf("TickOnce: %v", err)
	}

	dlv, err := s.Deliveries().GetByID(ctx, s.Pool().Pgx(), delID)
	if err != nil || dlv == nil {
		t.Fatalf("get delivery: %v", err)
	}
	if dlv.Status != repos.DeliveryDead {
		t.Errorf("status: got %q want dead", dlv.Status)
	}

	var n int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM dead_letters WHERE source_id=$1 AND subscription_id=$2`,
		delID, subID,
	).Scan(&n); err != nil {
		t.Fatalf("count dead_letters: %v", err)
	}
	if n != 1 {
		t.Fatalf("dead_letters: want 1, got %d", n)
	}
}

// TestIntegrationSchedulerConcurrentSkipLocked: two scheduler workers
// running against the same pool of pending deliveries must each claim
// distinct rows. The total dispatch count must equal the row count
// (no double-dispatch, no lost rows).
func TestIntegrationSchedulerConcurrentSkipLocked(t *testing.T) {
	t.Parallel()
	url := startPostgres(t)
	s := newTestStorage(t, url)
	ctx := context.Background()

	// Seed 20 deliveries across 4 subscriptions (5 each).
	const numSubs = 4
	const perSub = 5
	const total = numSubs * perSub

	seedAuthClient(t, s, "client-Z")
	for i := 0; i < numSubs; i++ {
		subID, err := s.Subscriptions().Insert(ctx, s.Pool().Pgx(), repos.SubscriptionRow{
			ClientID:    "client-Z",
			Status:      repos.SubActive,
			TopicURL:    "http://example.org/t",
			ChannelType: "rest-hook-fake",
			Endpoint:    fmt.Sprintf("https://sub-%d.example/", i),
			Content:     "id-only",
		})
		if err != nil {
			t.Fatalf("insert sub: %v", err)
		}
		for j := 0; j < perSub; j++ {
			ehrID, _, err := s.EhrEvents().Insert(ctx, s.Pool().Pgx(), repos.EhrEventRow{
				TopicURL: "http://example.org/t", Focus: fmt.Sprintf("X/%d-%d", i, j),
				ChangeKind: repos.ChangeCreate,
				Resource:   []byte(`{"resourceType":"X"}`), CorrelationID: uuid.New(),
				OccurredAt: time.Now().UTC(), ResourceChangeID: uuid.New(),
			})
			if err != nil {
				t.Fatalf("insert ehr_events: %v", err)
			}
			if _, err := s.Deliveries().Insert(ctx, s.Pool().Pgx(), repos.DeliveryRow{
				SubscriptionID: subID, EhrEventID: ehrID, EventNumber: int64(j + 1),
				Status: repos.DeliveryPending, NextAttemptAt: time.Now().UTC().Add(-time.Second),
				CorrelationID: uuid.New(),
			}); err != nil {
				t.Fatalf("insert deliveries: %v", err)
			}
		}
	}

	reg := scheduler.NewMapRegistry()
	wg := &sync.WaitGroup{}
	wg.Add(total)
	fake := &fakeChannel{wg: wg}
	reg.Register("rest-hook-fake", fake)

	const numWorkers = 4
	workers := make([]*scheduler.Worker, numWorkers)
	for i := range workers {
		workers[i] = newScheduler(t, s, reg, scheduler.RetryConfig{MaxAttempts: 5})
	}

	var ticks atomic.Int64
	var wgRun sync.WaitGroup
	wgRun.Add(numWorkers)
	stop := make(chan struct{})
	for i := range workers {
		w := workers[i]
		go func() {
			defer wgRun.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, err := w.TickOnce(ctx)
				if err != nil {
					t.Errorf("TickOnce: %v", err)
					return
				}
				ticks.Add(1)
			}
		}()
	}

	// Wait for every delivery to dispatch (or fail loud after a
	// generous timeout).
	dispatchDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(dispatchDone)
	}()
	select {
	case <-dispatchDone:
	case <-time.After(30 * time.Second):
		close(stop)
		t.Fatalf("timed out waiting for %d dispatches; got %d", total, fake.callCount())
	}
	close(stop)
	wgRun.Wait()

	if got := fake.callCount(); got != total {
		t.Fatalf("dispatch count: got %d want %d (no double-dispatch invariant)", got, total)
	}

	// All deliveries should be 'delivered'.
	var nDelivered int
	if err := s.Pool().Pgx().QueryRow(ctx,
		`SELECT count(*) FROM deliveries WHERE status='delivered'`,
	).Scan(&nDelivered); err != nil {
		t.Fatalf("count delivered: %v", err)
	}
	if nDelivered != total {
		t.Fatalf("delivered count: got %d want %d", nDelivered, total)
	}
}
