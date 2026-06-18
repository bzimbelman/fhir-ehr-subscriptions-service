// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/observability/correlation"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

// Config tunes the Worker's claim loop.
type Config struct {
	// ClaimBatchSize bounds the number of ehr_events rows a single
	// transaction claims. Default 1 (LLD §3 "Internal data
	// structures": every loop is a fresh claim).
	ClaimBatchSize int32
	// IdlePollInterval is the delay between empty-claim ticks.
	// Default 200ms.
	IdlePollInterval time.Duration
	// DBBackoffInitial / DBBackoffMax bound transient DB-error
	// backoff. Same shape as the matcher worker.
	DBBackoffInitial time.Duration
	DBBackoffMax     time.Duration
	// FilterErrorIsTransient: when true, EvaluationError on filterBy
	// for any candidate aborts the whole event so it stays unprocessed
	// and the next tick retries it. The LLD defaults to "skip the
	// subscription, keep going" — so the default here is false.
	FilterErrorIsTransient bool
}

// ApplyDefaults fills zero values per the LLD.
func (c *Config) ApplyDefaults() {
	if c.ClaimBatchSize == 0 {
		c.ClaimBatchSize = 1
	}
	if c.IdlePollInterval == 0 {
		c.IdlePollInterval = 200 * time.Millisecond
	}
	if c.DBBackoffInitial == 0 {
		c.DBBackoffInitial = 100 * time.Millisecond
	}
	if c.DBBackoffMax == 0 {
		c.DBBackoffMax = 5 * time.Second
	}
}

// Metrics is the metrics seam between the worker and the host's
// observability stack. The worker emits typed events; the host adapts
// to its sink. Keeping it narrow lets unit and integration tests pin
// expectations.
type Metrics interface {
	// FanoutOutcome counts one (topic, decision) pair per call.
	FanoutOutcome(topicURL string, decision FanoutDecision)
	// EventProcessed counts one ehr_events row claimed and committed.
	// matched is the number of deliveries rows the worker wrote in
	// the transaction.
	EventProcessed(topicURL string, matched int)
	// FilterRuntimeError is emitted for each subscription whose
	// filterBy hit an EvaluationError. Mirrors
	// fhir_subs_filter_runtime_errors_total in the LLD §metrics.
	FilterRuntimeError(subscriptionID uuid.UUID)
}

// nopMetrics satisfies Metrics with no-op methods. The default when no
// emitter is configured.
type nopMetrics struct{}

func (nopMetrics) FanoutOutcome(string, FanoutDecision) {}
func (nopMetrics) EventProcessed(string, int)           {}
func (nopMetrics) FilterRuntimeError(uuid.UUID)         {}

// Worker is the Stage 3 claim/fanout/commit loop.
//
// Each iteration is one transaction:
//
//  1. Claim up to ClaimBatchSize unprocessed ehr_events rows under
//     FOR UPDATE SKIP LOCKED.
//  2. For each row, list active subscriptions on its topic.
//  3. Evaluate each subscription's filterBy; for every Match, INSERT a
//     deliveries row keyed by (subscription_id, event_number) where
//     event_number is the per-subscription monotonic sequence
//     (max(deliveries.event_number)+1 per ADR 0010 #2).
//  4. Mark the ehr_events row processed.
//  5. Commit.
//
// A crashed worker leaves processed=false; another worker reclaims via
// SKIP LOCKED. The (subscription_id, event_number) UNIQUE on
// deliveries (combined with the deterministic max+1 assignment inside
// one transaction) makes any single-row replay idempotent.
type Worker struct {
	pool    *pgxpool.Pool
	subs    *repos.SubscriptionsRepo
	ehr     *repos.EhrEventsRepo
	dlv     *repos.DeliveriesRepo
	metrics Metrics
	logger  *slog.Logger
	clock   func() time.Time
	cfg     Config
}

// NewWorker constructs a Worker. The clock argument is optional; nil
// uses time.Now. metrics may be nil (nop). logger may be nil (slog
// default).
func NewWorker(
	pool *pgxpool.Pool,
	subs *repos.SubscriptionsRepo,
	ehr *repos.EhrEventsRepo,
	dlv *repos.DeliveriesRepo,
	cfg Config,
	opts ...Option,
) *Worker {
	cfg.ApplyDefaults()
	w := &Worker{
		pool:    pool,
		subs:    subs,
		ehr:     ehr,
		dlv:     dlv,
		cfg:     cfg,
		metrics: nopMetrics{},
		clock:   time.Now,
		logger:  slog.Default(),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Option configures a Worker at construction time. Keeping option-style
// because Metrics, Logger, and Clock are independent and tests want to
// override them in any combination.
type Option func(*Worker)

// WithMetrics installs a Metrics emitter.
func WithMetrics(m Metrics) Option {
	return func(w *Worker) {
		if m != nil {
			w.metrics = m
		}
	}
}

// WithLogger installs a slog logger.
func WithLogger(l *slog.Logger) Option {
	return func(w *Worker) {
		if l != nil {
			w.logger = l
		}
	}
}

// WithClock installs a clock function (test injection).
func WithClock(now func() time.Time) Option {
	return func(w *Worker) {
		if now != nil {
			w.clock = now
		}
	}
}

// Run blocks until ctx is canceled.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("submatcher: nil worker")
	}
	backoff := w.cfg.DBBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}
		processed, err := w.tickOnce(ctx)
		if err != nil {
			w.logger.WarnContext(ctx, "submatcher tick error", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = nextBackoff(backoff, w.cfg.DBBackoffMax)
			continue
		}
		backoff = w.cfg.DBBackoffInitial
		if !processed {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(w.cfg.IdlePollInterval):
			}
		}
	}
}

// TickOnce runs one claim/fanout/commit iteration. Exported so tests
// can drive the worker deterministically without a long-lived
// goroutine.
func (w *Worker) TickOnce(ctx context.Context) (bool, error) {
	return w.tickOnce(ctx)
}

func (w *Worker) tickOnce(ctx context.Context) (bool, error) {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("submatcher: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	rows, err := w.ehr.ClaimUnprocessed(ctx, tx, w.cfg.ClaimBatchSize)
	if err != nil {
		return false, fmt.Errorf("submatcher: claim: %w", err)
	}
	if len(rows) == 0 {
		_ = tx.Rollback(ctx)
		committed = true
		return false, nil
	}

	for i := range rows {
		if err := w.fanoutOne(ctx, tx, &rows[i]); err != nil {
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("submatcher: commit: %w", err)
	}
	committed = true
	return true, nil
}

// fanoutOne is the per-event fanout. Runs entirely inside the caller's
// transaction (the outbox).
func (w *Worker) fanoutOne(ctx context.Context, tx pgx.Tx, row *repos.EhrEventRow) error {
	cctx := correlation.WithID(ctx, row.CorrelationID.String())

	candidates, err := w.subs.ListActiveByTopic(cctx, tx, row.TopicURL)
	if err != nil {
		return fmt.Errorf("submatcher: list subs: %w", err)
	}

	event := EhrEvent{
		ID:               row.ID,
		EventNumber:      row.EventNumber,
		TopicURL:         row.TopicURL,
		ResourceType:     resourceTypeOf(row.Resource),
		ChangeKind:       string(row.ChangeKind),
		Focus:            row.Focus,
		Resource:         row.Resource,
		PreviousResource: row.PreviousResource,
		CorrelationID:    row.CorrelationID,
		OccurredAt:       row.OccurredAt,
		CreatedMonth:     row.CreatedMonth,
	}
	decisions := Evaluate(event, candidates)

	matched := 0
	for i := range decisions {
		d := &decisions[i]
		w.metrics.FanoutOutcome(row.TopicURL, d.Decision)
		switch d.Decision {
		case FanoutMatch:
			eventNum, err := nextEventNumber(cctx, tx, d.Subscription.ID)
			if err != nil {
				return fmt.Errorf("submatcher: next event number: %w", err)
			}
			if _, err := w.dlv.Insert(cctx, tx, repos.DeliveryRow{
				SubscriptionID: d.Subscription.ID,
				EhrEventID:     row.ID,
				EventNumber:    eventNum,
				Status:         repos.DeliveryPending,
				Attempts:       0,
				NextAttemptAt:  w.clock(),
				CorrelationID:  row.CorrelationID,
			}); err != nil {
				return fmt.Errorf("submatcher: insert delivery: %w", err)
			}
			// Advance the subscription's per-subscriber cursor in the
			// same transaction. Cursor is the wire-visible
			// eventsSinceSubscriptionStart; it must equal
			// MAX(event_number) for the subscription so that the next
			// fanout's GREATEST() compute is correct even if the
			// scheduler is behind on actual delivery. The LLD
			// distinguishes "delivered cursor" (advanced on Delivered)
			// from "pending cursor"; the storage schema only holds one
			// counter, which we treat as the pending counter at fanout
			// time. Scheduler is the one that records "delivered."
			if _, err := tx.Exec(cctx,
				`UPDATE subscriptions
				    SET events_since_subscription_start = GREATEST(events_since_subscription_start, $1),
				        updated_at = now()
				  WHERE id = $2`,
				eventNum, d.Subscription.ID,
			); err != nil {
				return fmt.Errorf("submatcher: advance cursor: %w", err)
			}
			matched++
		case FanoutEvaluationError:
			w.metrics.FilterRuntimeError(d.Subscription.ID)
			w.logger.WarnContext(cctx, "submatcher: filterBy evaluation error",
				slog.String("subscription_id", d.Subscription.ID.String()),
				slog.String("topic_url", row.TopicURL),
				slog.String("reason", d.SkipReason),
			)
			if w.cfg.FilterErrorIsTransient {
				return fmt.Errorf("submatcher: filter runtime error (transient): %s", d.SkipReason)
			}
		}
	}

	if _, err := w.ehr.MarkProcessed(cctx, tx, row.ID, row.CreatedMonth); err != nil {
		return fmt.Errorf("submatcher: mark processed: %w", err)
	}

	w.metrics.EventProcessed(row.TopicURL, matched)
	return nil
}

// nextEventNumber computes the per-subscription monotonic event_number
// per ADR 0010 #2: "deliveries.event_number — bigint, per-subscription
// monotonic, assigned at fanout (Stage 3). Computed as
// max(deliveries.event_number) + 1 WHERE subscription_id = ?".
func nextEventNumber(ctx context.Context, tx pgx.Tx, subID uuid.UUID) (int64, error) {
	var maxEvent *int64
	if err := tx.QueryRow(ctx,
		`SELECT MAX(event_number) FROM deliveries WHERE subscription_id = $1`, subID,
	).Scan(&maxEvent); err != nil {
		return 0, fmt.Errorf("submatcher: select max event_number: %w", err)
	}
	if maxEvent == nil {
		return 1, nil
	}
	return *maxEvent + 1, nil
}

// resourceTypeOf reads the top-level resourceType field. Used to fill
// EhrEvent.ResourceType when the row does not carry it explicitly. The
// ehr_events table does not have its own resource_type column — it is
// implicit in the resource body.
func resourceTypeOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	// Avoid a full unmarshal for what is usually a tiny field at the
	// top of the body. A cheap scan for "resourceType":"..." would do,
	// but the matcher already pays json.Unmarshal cost downstream so
	// one more here is acceptable.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if rt, ok := m["resourceType"].(string); ok {
		return rt
	}
	return ""
}

func nextBackoff(cur, ceiling time.Duration) time.Duration {
	next := cur * 2
	if next > ceiling {
		return ceiling
	}
	return next
}
