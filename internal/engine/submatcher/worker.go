// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package submatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/correlation"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// bytesReader is bytes.NewReader wrapped to fit io.Reader naming in
// scanResourceType.
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// Config tunes the Worker's claim loop.
type Config struct {
	// PoolSize is the number of concurrent workers. Default 1.
	// Mirrors matcher.Config.PoolSize so the submatcher and matcher
	// expose the same API surface (S-12). Run.RunPool is the
	// supported entry point for spawning the pool; Run remains a
	// single-worker entry point.
	PoolSize int
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
	// MaxRowAttempts caps how many times a single ehr_events row may
	// fail the fanout transaction before it gets dead-lettered
	// (S-12). Default 8. Without this a poison row pins the worker.
	MaxRowAttempts int32
	// CursorAdvanceBatchSize bounds how many (subscription_id,
	// event_number) pairs are flushed in a single batched UPDATE of
	// `events_since_subscription_start` (story #56 — S-12.4). Default
	// 1000. Postgres caps each extended-protocol bind at 65535
	// parameters and the planner cost of a giant unnest grows with
	// the array length, so we cap it to keep the per-flush UPDATE
	// snappy on hot topics.
	CursorAdvanceBatchSize int
}

// ApplyDefaults fills zero values per the LLD.
func (c *Config) ApplyDefaults() {
	if c.PoolSize == 0 {
		c.PoolSize = 1
	}
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
	if c.MaxRowAttempts == 0 {
		c.MaxRowAttempts = 8
	}
	if c.CursorAdvanceBatchSize == 0 {
		c.CursorAdvanceBatchSize = 1000
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
	// RowAttempt bumps fhir_subs_submatcher_row_attempts_total{outcome}
	// once per tickOnce return. outcome ∈ {processed, deferred, error};
	// "deferred" is the empty-claim short-circuit (no rows to fan out).
	// Mirrors matcher.MetricsEmitter.RowAttempt so cross-pipeline
	// dashboards line up (story #61, S-12.3).
	RowAttempt(outcome string)
}

// nopMetrics satisfies Metrics with no-op methods. The default when no
// emitter is configured.
type nopMetrics struct{}

func (nopMetrics) FanoutOutcome(string, FanoutDecision) {}
func (nopMetrics) EventProcessed(string, int)           {}
func (nopMetrics) FilterRuntimeError(uuid.UUID)         {}
func (nopMetrics) RowAttempt(string)                    {}

// AuthRechecker is the SPI the worker consumes at delivery prep
// (P2.7). It is satisfied by internal/api/auth.Rechecker (and by its
// CachedRechecker wrapper). Defined locally so the submatcher does
// not pull in the api/auth package's HTTP-handler graph; the wiring
// layer adapts auth.Rechecker into this interface.
//
// Recheck returns true when the subscription's owning client is still
// authorized to receive deliveries. An error short-circuits to true
// (fail-open) — a transient auth-store outage must not stop a healthy
// pipeline. The wiring layer is expected to wrap the Rechecker in a
// TTL cache so the auth store sees one call per subscription per
// (configurable) window, not one per fanout.
type AuthRechecker interface {
	Recheck(ctx context.Context, clientID, subscriptionID string) (bool, error)
}

// alwaysActiveAuth is the default AuthRechecker: every call returns
// true. Production wiring overrides via WithAuthRechecker.
type alwaysActiveAuth struct{}

func (alwaysActiveAuth) Recheck(context.Context, string, string) (bool, error) {
	return true, nil
}

// SubscriptionStateUpdater transitions a subscription to a terminal
// state when the auth re-check returns Revoked (P2.7). The worker
// invokes this in the same fanout transaction so the state change
// commits atomically with the absence of the (would-be) deliveries
// row. nil = no-op (test default; production wiring installs a
// repos-backed implementation).
type SubscriptionStateUpdater interface {
	// MarkErrorRevoked transitions the subscription to status='error'
	// with a reason that names the revocation. The submatcher passes
	// the existing transaction so the state change is part of the
	// same outbox commit as the (suppressed) fanout.
	MarkErrorRevoked(ctx context.Context, tx pgx.Tx, subscriptionID uuid.UUID, reason string) error
}

// subscriptionLister is the smallest contract the fanout loop needs
// from the subscriptions repo. Defined as an interface (not a concrete
// type) so unit tests can inject a fake that asserts the worker uses
// the streaming entry point — never the materializing
// ListActiveByTopic. *repos.SubscriptionsRepo satisfies it directly.
type subscriptionLister interface {
	StreamActiveByTopic(
		ctx context.Context, q repos.Querier, topicURL string,
		fn func(repos.SubscriptionRow) error,
	) error
}

// Worker is the Stage 3 claim/fanout/commit loop.
//
// Each iteration is one transaction:
//
//  1. Claim up to ClaimBatchSize unprocessed ehr_events rows under
//     FOR UPDATE SKIP LOCKED.
//  2. For each row, stream active subscriptions on its topic (story
//     #55 — the previous slice-materializing path was bounded by RAM
//     and pinned the fanout transaction open while the slice was
//     built).
//  3. Per streamed subscription, evaluate its filterBy; for a Match,
//     INSERT a deliveries row keyed by (subscription_id, event_number)
//     where event_number is the per-subscription monotonic sequence
//     (max(deliveries.event_number)+1 per ADR 0010 #2).
//  4. Mark the ehr_events row processed.
//  5. Commit.
//
// A crashed worker leaves processed=false; another worker reclaims via
// SKIP LOCKED. The (subscription_id, event_number) UNIQUE on
// deliveries (combined with the deterministic max+1 assignment inside
// one transaction) makes any single-row replay idempotent.
type Worker struct {
	pool         *pgxpool.Pool
	subs         subscriptionLister
	ehr          *repos.EhrEventsRepo
	dlv          *repos.DeliveriesRepo
	metrics      Metrics
	logger       *slog.Logger
	clock        func() time.Time
	cfg          Config
	authRecheck  AuthRechecker
	stateUpdater SubscriptionStateUpdater
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
		pool:        pool,
		subs:        subs,
		ehr:         ehr,
		dlv:         dlv,
		cfg:         cfg,
		metrics:     nopMetrics{},
		clock:       time.Now,
		logger:      slog.Default(),
		authRecheck: alwaysActiveAuth{},
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

// WithAuthRechecker installs the delivery-time scope re-check (P2.7).
// nil = keep the always-active default. Production wiring installs a
// CachedRechecker wrapping the auth-store implementation.
func WithAuthRechecker(r AuthRechecker) Option {
	return func(w *Worker) {
		if r != nil {
			w.authRecheck = r
		}
	}
}

// WithStateUpdater installs the SubscriptionStateUpdater used on a
// Revoked re-check (P2.7). nil = no-op (the worker still suppresses
// the deliveries insert and emits the FanoutAuthRevoked metric, but
// does not transition subscription state).
func WithStateUpdater(u SubscriptionStateUpdater) Option {
	return func(w *Worker) {
		if u != nil {
			w.stateUpdater = u
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
		w.metrics.RowAttempt("error")
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
		w.metrics.RowAttempt("error")
		return false, fmt.Errorf("submatcher: claim: %w", err)
	}
	if len(rows) == 0 {
		_ = tx.Rollback(ctx)
		committed = true
		w.metrics.RowAttempt("deferred")
		return false, nil
	}

	for i := range rows {
		if err := w.fanoutOne(ctx, tx, &rows[i]); err != nil {
			w.metrics.RowAttempt("error")
			return false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		w.metrics.RowAttempt("error")
		return false, fmt.Errorf("submatcher: commit: %w", err)
	}
	committed = true
	w.metrics.RowAttempt("processed")
	return true, nil
}

// fanoutOne is the per-event fanout. Runs entirely inside the caller's
// transaction (the outbox).
//
// Story #55: streams active subscriptions for the topic via
// StreamActiveByTopic and applies per-row evaluation + side effects in
// the same pgx.Rows iteration loop. The previous implementation
// materialized every active subscription into a slice up-front; on a
// hot topic that pinned the transaction open until the entire result
// set was buffered, with peak memory O(N_active). The streaming path
// keeps peak memory flat regardless of N: at any moment the loop
// holds one SubscriptionRow plus the result of evaluating it.
func (w *Worker) fanoutOne(ctx context.Context, tx pgx.Tx, row *repos.EhrEventRow) error {
	cctx := correlation.WithID(ctx, row.CorrelationID.String())

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

	matched := 0
	// Per-subscription cursor batch (story #56 — S-12.4). The pre-#56
	// fanout issued one inline UPDATE of events_since_subscription_start
	// per Match, so a topic with N hot subscriptions paid N UPDATE
	// round-trips inside the fanout transaction. We accumulate
	// (subscription_id, event_number) pairs while streaming and flush
	// them via a single batched UPDATE keyed on
	// `unnest($1::uuid[], $2::bigint[])`. The cap (CursorAdvanceBatchSize)
	// keeps the per-flush UPDATE snappy and well under Postgres' 65535
	// extended-protocol bind-parameter ceiling on truly hot topics.
	batchCap := w.cfg.CursorAdvanceBatchSize
	cursorIDs := make([]uuid.UUID, 0, batchCap)
	cursorNums := make([]int64, 0, batchCap)
	flushCursor := func() error {
		if len(cursorIDs) == 0 {
			return nil
		}
		if err := batchedAdvanceCursor(cctx, tx, cursorIDs, cursorNums); err != nil {
			return err
		}
		cursorIDs = cursorIDs[:0]
		cursorNums = cursorNums[:0]
		return nil
	}
	// Reusable single-element slice fed to Evaluate so the pure
	// evaluator's signature stays unchanged but we never grow a
	// per-topic slab.
	one := make([]repos.SubscriptionRow, 1)
	if err := w.subs.StreamActiveByTopic(cctx, tx, row.TopicURL, func(sub repos.SubscriptionRow) error {
		one[0] = sub
		decisions := Evaluate(event, one)
		if len(decisions) != 1 {
			return fmt.Errorf("submatcher: evaluate produced %d decisions (want 1)", len(decisions))
		}
		d := decisions[0]
		// P2.7: delivery-time scope re-check. We layer this on top of
		// Evaluate so the pure evaluator stays free of I/O. Only Match
		// decisions need a re-check; other decisions are already
		// short-circuiting the deliveries insert.
		if d.Decision == FanoutMatch && w.authRecheck != nil {
			active, err := w.authRecheck.Recheck(cctx, d.Subscription.ClientID, d.Subscription.ID.String())
			switch {
			case err != nil:
				// Fail-open: a transient auth-store outage must not
				// stop a healthy pipeline. We log and proceed with
				// the original Match.
				w.logger.WarnContext(cctx, "submatcher: auth recheck error (fail-open)",
					slog.String("subscription_id", d.Subscription.ID.String()),
					slog.String("client_id", d.Subscription.ClientID),
					slog.String("err", err.Error()),
				)
			case !active:
				d.Decision = FanoutAuthRevoked
				d.SkipReason = "auth_revoked"
			}
		}
		w.metrics.FanoutOutcome(row.TopicURL, d.Decision)
		switch d.Decision {
		case FanoutAuthRevoked:
			// Suppress the fanout; flip the subscription to
			// status='error' atomically with the absence of the
			// deliveries row. The state-updater is optional so unit
			// tests that don't construct the repos still get the
			// metric and the suppression.
			if w.stateUpdater != nil {
				if err := w.stateUpdater.MarkErrorRevoked(cctx, tx, d.Subscription.ID, "auth_revoked"); err != nil {
					return fmt.Errorf("submatcher: mark error revoked: %w", err)
				}
			}
			w.logger.InfoContext(cctx, "submatcher: subscription auth revoked at delivery prep",
				slog.String("subscription_id", d.Subscription.ID.String()),
				slog.String("client_id", d.Subscription.ClientID),
				slog.String("topic_url", row.TopicURL),
			)
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
			// same transaction. Story #56 (S-12.4): we accumulate the
			// (subscription_id, event_number) pair and flush in one
			// batched UPDATE per CursorAdvanceBatchSize (or once at
			// the end of streaming, whichever comes first). The
			// cursor is the wire-visible eventsSinceSubscriptionStart
			// and must equal MAX(event_number) for the subscription so
			// the next fanout's GREATEST() compute stays correct even
			// if the scheduler is behind on actual delivery. The
			// batched form preserves the GREATEST semantics per row.
			cursorIDs = append(cursorIDs, d.Subscription.ID)
			cursorNums = append(cursorNums, eventNum)
			if len(cursorIDs) >= batchCap {
				if err := flushCursor(); err != nil {
					return err
				}
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
		return nil
	}); err != nil {
		return fmt.Errorf("submatcher: stream subs: %w", err)
	}

	// Trailing flush of any pairs accumulated since the last cap-fill
	// (or the only flush, when N <= cap).
	if err := flushCursor(); err != nil {
		return err
	}

	if _, err := w.ehr.MarkProcessed(cctx, tx, row.ID, row.CreatedMonth); err != nil {
		return fmt.Errorf("submatcher: mark processed: %w", err)
	}

	w.metrics.EventProcessed(row.TopicURL, matched)
	return nil
}

// batchedAdvanceCursor flushes a (subscription_id, event_number) batch
// into one UPDATE statement on `subscriptions`. The form
//
//	UPDATE subscriptions
//	   SET events_since_subscription_start =
//	         GREATEST(events_since_subscription_start, batch.event_number),
//	       updated_at = now()
//	  FROM unnest($1::uuid[], $2::bigint[]) AS batch(id, event_number)
//	 WHERE subscriptions.id = batch.id
//
// preserves the per-row GREATEST semantics of the pre-#56 inline
// UPDATE while collapsing N round-trips into 1. Story #56 — S-12.4.
func batchedAdvanceCursor(ctx context.Context, tx pgx.Tx, ids []uuid.UUID, nums []int64) error {
	if len(ids) != len(nums) {
		return fmt.Errorf("submatcher: cursor batch length mismatch: %d ids vs %d nums", len(ids), len(nums))
	}
	if len(ids) == 0 {
		return nil
	}
	const sql = `
		UPDATE subscriptions
		   SET events_since_subscription_start = GREATEST(subscriptions.events_since_subscription_start, batch.event_number),
		       updated_at = now()
		  FROM unnest($1::uuid[], $2::bigint[]) AS batch(id, event_number)
		 WHERE subscriptions.id = batch.id`
	if _, err := tx.Exec(ctx, sql, ids, nums); err != nil {
		return fmt.Errorf("submatcher: advance cursor: %w", err)
	}
	return nil
}

// nextEventNumber returns the next per-subscription monotonic
// event_number under SELECT FOR UPDATE on subscriptions.next_event_number.
//
// Why not MAX(deliveries.event_number)+1: (a) two workers could observe
// the same MAX and both INSERT N+1, hitting the (subscription_id,
// event_number) UNIQUE; (b) once retention deletes old deliveries the
// MAX drops, and the next fanout reuses a number that subscribers have
// already replayed past. The persistent counter on subscriptions sidesteps
// both — row-level locking serializes contention and deletion of
// deliveries cannot regress the cursor (audit B-26, B-27).
func nextEventNumber(ctx context.Context, tx pgx.Tx, subID uuid.UUID) (int64, error) {
	const sql = `
		UPDATE subscriptions
		   SET next_event_number = next_event_number + 1,
		       updated_at = now()
		 WHERE id = $1
		 RETURNING next_event_number`
	var n int64
	if err := tx.QueryRow(ctx, sql, subID).Scan(&n); err != nil {
		return 0, fmt.Errorf("submatcher: advance next_event_number: %w", err)
	}
	return n, nil
}

// resourceTypeOf reads the top-level resourceType field. Used to fill
// EhrEvent.ResourceType when the row does not carry it explicitly. The
// ehr_events table does not have its own resource_type column — it is
// implicit in the resource body.
//
// S-12: was a full json.Unmarshal-into-map[string]any which costs
// ~100x more than scanning for the field directly. Use a streaming
// decoder limited to the first object key + value so we do not pay
// for the entire body. Falls back to the original full-decode path
// when the streaming scan does not find the field.
func resourceTypeOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	if rt := scanResourceType(body); rt != "" {
		return rt
	}
	// Fallback: full decode. Pays the cost only on weird shapes the
	// streaming scanner cannot handle (e.g., resourceType nested
	// inside an envelope), so the common path still benefits.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	if rt, ok := m["resourceType"].(string); ok {
		return rt
	}
	return ""
}

// scanResourceType uses a json.Decoder streaming over body to find
// the first top-level "resourceType" key and return its string value
// without materializing the rest of the body (S-12).
func scanResourceType(body []byte) string {
	dec := json.NewDecoder(bytesReader(body))
	tok, err := dec.Token()
	if err != nil {
		return ""
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return ""
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ""
		}
		key, _ := keyTok.(string)
		valTok, err := dec.Token()
		if err != nil {
			return ""
		}
		if key == "resourceType" {
			s, _ := valTok.(string)
			return s
		}
		// If the value is a delimiter (object/array), skip it.
		if d, ok := valTok.(json.Delim); ok {
			if err := skipJSONValue(dec, d); err != nil {
				return ""
			}
		}
	}
	return ""
}

// skipJSONValue consumes balanced delimiters so the decoder advances
// past a nested object/array.
func skipJSONValue(dec *json.Decoder, open json.Delim) error {
	depth := 1
	for depth > 0 {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok {
			if d == open || (open == '{' && d == '[') || (open == '[' && d == '{') {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

func nextBackoff(cur, ceiling time.Duration) time.Duration {
	next := cur * 2
	if next > ceiling {
		return ceiling
	}
	return next
}
