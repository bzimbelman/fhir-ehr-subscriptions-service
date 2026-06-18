// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package heartbeat is the production worker for periodic heartbeat
// notifications (P2.6 MVP). For each active subscription whose
// `heartbeat_period > 0` and which has not produced a delivery within
// `heartbeat_period`, the worker writes a heartbeat-bundle delivery so
// the channel layer fans it out via the configured transport.
//
// MVP scope:
//   - One worker per process; per-tick scan over active subscriptions
//     with non-zero heartbeat_period.
//   - Last-activity check uses the subscription's `updated_at` as the
//     proxy for "last delivery"; this is conservative — a subscription
//     state-update bumps updated_at and pushes the heartbeat back. A
//     more precise check (MAX(deliveries.created_at)) is post-MVP and
//     requires a query the repo doesn't expose yet.
//   - Heartbeat deliveries piggyback on the existing scheduler: the
//     worker inserts a deliveries row with a synthetic notification
//     bundle (BundleKind=heartbeat) and the scheduler dispatches it
//     like any other delivery. The builder already produces the
//     correct heartbeat Bundle shape (see internal/engine/builder).
//
// Out of scope (post-MVP):
//   - State-machine for subscription transitions (requested → active →
//     error → off) that emits handshake notifications. Today the
//     activation handshake fires on rest-hook only (D-2); websocket
//     and email activators remain placeholders.
//   - Real websocket activator (token-issued path; client binds via
//     $get-ws-binding-token and the activator resolves the bind on
//     first connect). The MVP keeps the placeholder activator.
//   - Real email activator (relay-side AUTH semantics; potentially
//     asynchronous via outcome_sink).
//   - Per-subscription jitter / backoff to avoid thundering-herd on a
//     deployment with many subscriptions sharing the same period.
//   - Skipping a heartbeat when a real delivery is already pending
//     (today the worker only checks updated_at; a pending delivery
//     does not always update it).
package heartbeat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Querier is the subset of operations the worker needs from a Postgres
// connection or pool. Defined locally to keep the heartbeat tests free
// of testcontainers; production wiring satisfies it with *pgxpool.Pool.
type Querier interface {
	// CandidatesDueForHeartbeat returns the IDs of active subscriptions
	// whose heartbeat_period > 0 and whose updated_at is older than
	// (now - heartbeat_period). Implementations must bound the result
	// (e.g., LIMIT 100) so a backlogged worker does not load every
	// subscription in one tick.
	CandidatesDueForHeartbeat(ctx context.Context, now time.Time, limit int) ([]Candidate, error)
	// EnqueueHeartbeat writes the synthetic heartbeat delivery row.
	// The subsequent scheduler tick fans it out.
	EnqueueHeartbeat(ctx context.Context, subscriptionID uuid.UUID, now time.Time) error
}

// Candidate is a subscription due for a heartbeat. The ID is enough
// for the worker; the storage layer carries the rest of the row.
type Candidate struct {
	ID uuid.UUID
}

// Worker is the heartbeat scheduler. Construct with New.
type Worker struct {
	q              Querier
	clock          func() time.Time
	tickInterval   time.Duration
	candidateLimit int
}

// Options configures New.
type Options struct {
	// Querier is required.
	Querier Querier
	// Clock is the time source; nil = time.Now.
	Clock func() time.Time
	// TickInterval is how often the worker scans for due heartbeats.
	// Zero falls back to 30 seconds. The smaller this value, the
	// finer-grained the heartbeat punctuality (at the cost of more
	// scan queries). Operators typically want this an order of
	// magnitude below the smallest configured heartbeat_period.
	TickInterval time.Duration
	// CandidateLimit bounds the number of subscriptions a single tick
	// processes. Zero falls back to 100.
	CandidateLimit int
}

// New constructs a Worker.
func New(opts Options) (*Worker, error) {
	if opts.Querier == nil {
		return nil, errors.New("heartbeat: Querier is required")
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	tick := opts.TickInterval
	if tick <= 0 {
		tick = 30 * time.Second
	}
	limit := opts.CandidateLimit
	if limit <= 0 {
		limit = 100
	}
	return &Worker{
		q:              opts.Querier,
		clock:          clock,
		tickInterval:   tick,
		candidateLimit: limit,
	}, nil
}

// Run blocks until ctx is canceled. Each tick scans for due
// subscriptions and enqueues a heartbeat delivery for each.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.tickInterval)
	defer t.Stop()
	// Run an immediate tick so a deployment with a hot subscription
	// list gets a heartbeat without waiting a full interval.
	_, _ = w.TickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if _, err := w.TickOnce(ctx); err != nil {
				// Errors are observable to operators via metrics in
				// production wiring; for the MVP we keep going so a
				// transient query failure doesn't stop heartbeats
				// for every subscription. (Real wiring should bump a
				// fhir_subs_heartbeat_tick_errors_total counter.)
				continue
			}
		}
	}
}

// TickOnce performs one scan/enqueue iteration and returns the count
// of heartbeats enqueued. Exported so tests can drive the worker
// deterministically.
func (w *Worker) TickOnce(ctx context.Context) (int, error) {
	now := w.clock()
	candidates, err := w.q.CandidatesDueForHeartbeat(ctx, now, w.candidateLimit)
	if err != nil {
		return 0, err
	}
	emitted := 0
	for _, c := range candidates {
		if err := w.q.EnqueueHeartbeat(ctx, c.ID, now); err != nil {
			// Skip this one and keep going; a single bad row must not
			// stop heartbeats for every other subscription.
			continue
		}
		emitted++
	}
	return emitted, nil
}
