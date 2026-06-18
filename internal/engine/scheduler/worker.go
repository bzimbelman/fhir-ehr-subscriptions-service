// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/correlation"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Config tunes the scheduler claim/dispatch loop.
type Config struct {
	// ClaimBatchSize bounds the number of deliveries rows a single
	// claim transaction picks up. LLD §"Configuration knobs"
	// suggests 64.
	ClaimBatchSize int32
	// IdlePollInterval is the delay between empty-claim ticks.
	IdlePollInterval time.Duration
	// DBBackoffInitial / DBBackoffMax bound transient DB-error
	// backoff.
	DBBackoffInitial time.Duration
	DBBackoffMax     time.Duration
	// Retry tunes the retry curve. See ComputeBackoff.
	Retry RetryConfig
	// ShutdownGrace bounds how long Run waits for in-flight
	// dispatchOne calls to finish after ctx is canceled. Default 10s.
	ShutdownGrace time.Duration
	// RecoveryInterval is how often the recovery sweep runs to reset
	// rows that have been stuck in 'delivering' beyond StuckThreshold
	// (e.g., after a worker crash). Default 30s.
	RecoveryInterval time.Duration
	// StuckThreshold is the minimum age a 'delivering' row must reach
	// before the recovery sweep flips it back to 'pending'. Default 5m.
	StuckThreshold time.Duration
	// DispatchConcurrency bounds how many dispatchOne calls per batch
	// run in parallel. Default 1 (legacy serial behavior). Increase
	// to amortize one slow channel.Deliver across the rest of the
	// batch (S-8.1). Capped at the batch size internally.
	DispatchConcurrency int
}

// applyDefaults fills zero values per the LLD's "Configuration knobs".
func (c *Config) applyDefaults() {
	if c.ClaimBatchSize == 0 {
		c.ClaimBatchSize = 64
	}
	if c.IdlePollInterval == 0 {
		c.IdlePollInterval = time.Second
	}
	if c.DBBackoffInitial == 0 {
		c.DBBackoffInitial = 100 * time.Millisecond
	}
	if c.DBBackoffMax == 0 {
		c.DBBackoffMax = 5 * time.Second
	}
	if c.ShutdownGrace == 0 {
		c.ShutdownGrace = 10 * time.Second
	}
	if c.RecoveryInterval == 0 {
		c.RecoveryInterval = 30 * time.Second
	}
	if c.StuckThreshold == 0 {
		c.StuckThreshold = 5 * time.Minute
	}
	if c.DispatchConcurrency <= 0 {
		c.DispatchConcurrency = 1
	}
	c.Retry.applyDefaults()
}

// Metrics is the metrics seam.
type Metrics interface {
	// Outcome counts one (channel-id, action) pair per delivery
	// dispatch.
	Outcome(channelID string, action Action)
	// DeliveryDuration observes the wall-clock spent in
	// channel.Deliver.
	DeliveryDuration(channelID string, d time.Duration)
}

type nopMetrics struct{}

func (nopMetrics) Outcome(string, Action)                 {}
func (nopMetrics) DeliveryDuration(string, time.Duration) {}

// ChannelRegistry resolves a Channel by its registered name (the
// subscriptions.channel_type column). The scheduler does not call
// channel modules directly; it goes through the registry so the SPI
// contract (channel.Channel) is the only seam.
type ChannelRegistry interface {
	// Lookup returns the channel for the given channelType; ok=false
	// if no channel is registered for that name.
	Lookup(channelType string) (channel.Channel, bool)
}

// MapRegistry is a simple static registry for tests and small
// deployments. The scheduler accepts any ChannelRegistry.
type MapRegistry struct {
	mu       sync.RWMutex
	channels map[string]channel.Channel
}

// NewMapRegistry constructs an empty MapRegistry.
func NewMapRegistry() *MapRegistry {
	return &MapRegistry{channels: make(map[string]channel.Channel)}
}

// Register adds or replaces a channel under the given name.
func (r *MapRegistry) Register(name string, ch channel.Channel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[name] = ch
}

// Lookup implements ChannelRegistry.
func (r *MapRegistry) Lookup(name string) (channel.Channel, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ch, ok := r.channels[name]
	return ch, ok
}

// Worker is the Stage 5 claim/dispatch loop.
type Worker struct {
	pool     *pgxpool.Pool
	subs     *repos.SubscriptionsRepo
	ehr      *repos.EhrEventsRepo
	dlv      *repos.DeliveriesRepo
	dl       *repos.DeadLettersRepo
	registry ChannelRegistry
	bldr     *builder.Builder
	cfg      Config
	metrics  Metrics
	logger   *slog.Logger
	clock    func() time.Time
	rng      RNG
	// inflight tracks each in-flight dispatchOne so Run can wait for
	// them to finish before returning. Without this, a SIGTERM mid-tick
	// would leave deliveries rows pinned in 'delivering' forever; the
	// recovery sweep would clean them up but the row would skip past
	// its retry window meanwhile (audit B-31).
	inflight sync.WaitGroup
}

// Options configure a Worker.
type Options struct {
	Metrics Metrics
	Logger  *slog.Logger
	Clock   func() time.Time
	RNG     RNG
}

// NewWorker constructs a Worker.
func NewWorker(
	pool *pgxpool.Pool,
	subs *repos.SubscriptionsRepo,
	ehr *repos.EhrEventsRepo,
	dlv *repos.DeliveriesRepo,
	dl *repos.DeadLettersRepo,
	registry ChannelRegistry,
	bldr *builder.Builder,
	cfg Config,
	opts Options,
) *Worker {
	cfg.applyDefaults()
	w := &Worker{
		pool: pool, subs: subs, ehr: ehr, dlv: dlv, dl: dl,
		registry: registry, bldr: bldr, cfg: cfg,
		metrics: nopMetrics{},
		logger:  slog.Default(),
		clock:   time.Now,
	}
	if opts.Metrics != nil {
		w.metrics = opts.Metrics
	}
	if opts.Logger != nil {
		w.logger = opts.Logger
	}
	if opts.Clock != nil {
		w.clock = opts.Clock
	}
	w.rng = opts.RNG
	return w
}

// Run blocks until ctx is canceled.
//
// On shutdown, Run waits up to cfg.ShutdownGrace for any in-flight
// dispatchOne calls to drain. While the loop is running, a separate
// goroutine ticks the recovery sweep (resetRecoveryStuck) every
// cfg.RecoveryInterval to reset stale 'delivering' rows from prior
// crashed workers. Both behaviors are required by audit B-31.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil {
		return errors.New("scheduler: nil worker")
	}

	recCtx, recCancel := context.WithCancel(ctx)
	defer recCancel()
	go w.runRecoverySweep(recCtx)

	backoff := w.cfg.DBBackoffInitial
	stop := false
	for !stop {
		if ctx.Err() != nil {
			break
		}
		processed, err := w.tickOnce(ctx)
		if err != nil {
			w.logger.WarnContext(ctx, "scheduler tick error", slog.String("err", err.Error()))
			select {
			case <-ctx.Done():
				stop = true
			case <-time.After(backoff):
			}
			if stop {
				break
			}
			backoff = nextBackoff(backoff, w.cfg.DBBackoffMax)
			continue
		}
		backoff = w.cfg.DBBackoffInitial
		if !processed {
			select {
			case <-ctx.Done():
				stop = true
			case <-time.After(w.cfg.IdlePollInterval):
			}
		}
	}

	// ctx is canceled; allow already-launched dispatchOne goroutines a
	// bounded budget to finish their commit.
	done := make(chan struct{})
	go func() {
		w.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(w.cfg.ShutdownGrace):
		w.logger.WarnContext(ctx, "scheduler: shutdown grace exceeded; remaining in-flight dispatches will be reset by recovery sweep")
	}
	return nil
}

// TickOnce performs one claim/dispatch/handle iteration. Exported so
// tests can drive deterministically.
func (w *Worker) TickOnce(ctx context.Context) (bool, error) {
	return w.tickOnce(ctx)
}

func (w *Worker) tickOnce(ctx context.Context) (bool, error) {
	// 1. Claim a batch under FOR UPDATE SKIP LOCKED. The claim
	//    transaction immediately flips status to 'delivering' so a
	//    second worker (or a crash recovery sweep) cannot re-pick the
	//    row even though our connection releases the lock when we
	//    commit.
	claimTx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("scheduler: begin claim: %w", err)
	}
	rows, claimErr := w.dlv.ClaimPending(ctx, claimTx, w.cfg.ClaimBatchSize, w.clock())
	if claimErr != nil {
		_ = claimTx.Rollback(ctx)
		return false, fmt.Errorf("scheduler: claim: %w", claimErr)
	}
	if len(rows) == 0 {
		_ = claimTx.Rollback(ctx)
		return false, nil
	}
	if err := claimTx.Commit(ctx); err != nil {
		return false, fmt.Errorf("scheduler: commit claim: %w", err)
	}

	// 2. Dispatch each claimed row. Per-row work runs concurrently
	//    bounded by DispatchConcurrency so one slow channel call cannot
	//    head-of-line-block sibling rows in the same batch (S-8.1). We
	//    register each call on the in-flight WaitGroup so a Run
	//    shutdown can wait for them to commit before returning (B-31).
	concurrency := w.cfg.DispatchConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > len(rows) {
		concurrency = len(rows)
	}
	sem := make(chan struct{}, concurrency)
	for i := range rows {
		w.inflight.Add(1)
		sem <- struct{}{}
		go func(r *repos.DeliveryRow) {
			defer w.inflight.Done()
			defer func() { <-sem }()
			w.dispatchOne(ctx, r)
		}(&rows[i])
	}
	return true, nil
}

// runRecoverySweep periodically resets rows that have been pinned in
// 'delivering' for longer than StuckThreshold. Such rows are the
// fingerprint of a worker that claimed them and crashed before either
// committing the outcome or finishing its tick. Without this sweep
// they would never retry and the row would pile up in 'delivering'.
func (w *Worker) runRecoverySweep(ctx context.Context) {
	t := time.NewTicker(w.cfg.RecoveryInterval)
	defer t.Stop()
	// Run an initial sweep on startup so a fresh boot reclaims rows
	// that the previous process left dangling.
	w.recoverStuck(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.recoverStuck(ctx)
		}
	}
}

// RecoverStuckForTest is a thin testing seam over recoverStuck. Tests
// can drive a single recovery pass deterministically without standing
// up the long-lived Run loop.
func (w *Worker) RecoverStuckForTest(ctx context.Context) int64 {
	return w.recoverStuck(ctx)
}

// recoverStuck flips 'delivering' rows whose updated_at is older than
// StuckThreshold back to 'pending', incrementing attempts so the
// re-attempt is recorded. Returns the number of rows reset.
func (w *Worker) recoverStuck(ctx context.Context) int64 {
	if ctx.Err() != nil {
		return 0
	}
	cutoff := w.clock().Add(-w.cfg.StuckThreshold)
	const sql = `
		UPDATE deliveries
		   SET status = 'pending',
		       attempts = attempts + 1,
		       next_attempt_at = now(),
		       last_error = COALESCE(last_error, '') || CASE WHEN COALESCE(last_error,'')='' THEN '' ELSE '; ' END || 'recovery_sweep:stuck_in_delivering',
		       updated_at = now()
		 WHERE status = 'delivering' AND updated_at < $1`
	tag, err := w.pool.Exec(ctx, sql, cutoff)
	if err != nil {
		w.logger.WarnContext(ctx, "scheduler: recovery sweep failed", slog.Any("err", err))
		return 0
	}
	n := tag.RowsAffected()
	if n > 0 {
		w.logger.InfoContext(ctx, "scheduler: recovery sweep reset stuck rows", slog.Int64("count", n))
	}
	return n
}

// dispatchOne drives one claimed deliveries row through build →
// channel → outcome handle. Errors are logged + counted but do not
// propagate back to the loop — a stuck channel for one subscription
// must not block other deliveries.
func (w *Worker) dispatchOne(ctx context.Context, row *repos.DeliveryRow) {
	cctx := correlation.WithID(ctx, row.CorrelationID.String())

	sub, err := w.subs.GetByID(cctx, w.pool, row.SubscriptionID)
	// S-8.2: a not-found subscription (sub == nil with no err) is
	// permanent — the row has been deleted, retry cannot recover it.
	// A pure DB error (err != nil, sub == nil) stays transient.
	if err != nil || sub == nil {
		w.logger.ErrorContext(cctx, "scheduler: load subscription failed",
			slog.String("delivery_id", row.ID.String()), slog.Any("err", err))
		if err == nil && sub == nil {
			w.requeueWithReason(cctx, row, ReasonSubscriptionUnavailable)
		} else {
			w.requeueAsTransient(cctx, row, "subscription_load_error")
		}
		return
	}

	ev, err := w.loadEhrEvent(cctx, row.EhrEventID)
	if err != nil || ev == nil {
		w.logger.ErrorContext(cctx, "scheduler: load ehr_event failed",
			slog.String("delivery_id", row.ID.String()), slog.Any("err", err))
		if err == nil && ev == nil {
			w.requeueWithReason(cctx, row, ReasonEhrEventUnavailable)
		} else {
			w.requeueAsTransient(cctx, row, "ehr_event_load_error")
		}
		return
	}

	job := builder.Job{
		Subscription:     *sub,
		NotificationType: channel.BundleEventNotification,
		Events:           []repos.EhrEventRow{*ev},
		PerSubEventNumbers: map[uuid.UUID]int64{
			ev.ID: row.EventNumber,
		},
		Attempt:               attemptsToUint32(row.Attempts),
		CorrelationIDOverride: row.CorrelationID.String(),
	}
	envelope, err := w.bldr.Build(cctx, job)
	if err != nil {
		w.logger.ErrorContext(cctx, "scheduler: build envelope failed",
			slog.String("delivery_id", row.ID.String()), slog.Any("err", err))
		// S-8.3: deterministic build errors (missing/empty subscription
		// id, malformed resource) will never succeed on retry. Today
		// the only deterministic build error is "subscription has nil
		// id" — anything else falls through as transient.
		if isPermanentBuildError(err) {
			w.applyBuildDecision(cctx, row, ClassifyBuildError(BuildErrorPermanent, w.cfg.Retry, row.Attempts+1), "build_error_permanent: "+err.Error())
			return
		}
		w.requeueAsTransient(cctx, row, "build_error: "+err.Error())
		return
	}

	ch, ok := w.registry.Lookup(sub.ChannelType)
	if !ok {
		w.logger.ErrorContext(cctx, "scheduler: channel not registered",
			slog.String("channel_type", sub.ChannelType))
		w.requeueAsTransient(cctx, row, "channel_unavailable: "+sub.ChannelType)
		return
	}

	t0 := w.clock()
	chOutcome, deliverErr := ch.Deliver(cctx, envelope)
	w.metrics.DeliveryDuration(sub.ChannelType, w.clock().Sub(t0))
	if deliverErr != nil {
		// Pre-flight error from the channel (e.g., invalid envelope) —
		// treat as transient with a small backoff. The channel SPI
		// reserves err for setup-time problems.
		//
		// N-1: setup errors flow through the standard transient retry
		// budget (RetryConfig.MaxAttempts), so the row dead-letters on
		// the Nth attempt rather than spinning forever. A future
		// enhancement could classify-as-permanent the same setup-error
		// string repeating across consecutive attempts; until then
		// the channel module is responsible for not returning a
		// retry-able error for genuinely permanent setup failures.
		w.logger.ErrorContext(cctx, "scheduler: channel deliver setup error",
			slog.String("channel_type", sub.ChannelType), slog.Any("err", deliverErr))
		w.requeueAsTransient(cctx, row, "channel_setup_error: "+deliverErr.Error())
		return
	}

	outcome := FromChannelOutcome(chOutcome)
	postAttempts := row.Attempts + 1
	decision := ClassifyOutcomeForChannel(outcome, w.cfg.Retry, sub.ChannelType, postAttempts)
	w.metrics.Outcome(sub.ChannelType, decision.Action)

	if err := w.applyDecision(cctx, row, sub, outcome, decision, postAttempts); err != nil {
		w.logger.ErrorContext(cctx, "scheduler: apply decision failed",
			slog.String("action", decision.Action.String()), slog.Any("err", err))
	}
}

// applyDecision runs one transaction to record the outcome on the
// deliveries row, advance the subscription cursor (Delivered only),
// and append a dead_letters row when the action requires it.
func (w *Worker) applyDecision(
	ctx context.Context,
	row *repos.DeliveryRow,
	sub *repos.SubscriptionRow,
	outcome OutcomeFromChannel,
	decision Decision,
	postAttempts int32,
) error {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	switch decision.Action {
	case ActionMarkDelivered:
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'delivered', attempts = $2, last_error = NULL, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts,
		); err != nil {
			return fmt.Errorf("mark delivered: %w", err)
		}
		// Advance the subscription cursor to the per-subscription
		// event_number this delivery represents (not lower than the
		// existing cursor — GREATEST guards against an out-of-order
		// late commit).
		if _, err := tx.Exec(ctx,
			`UPDATE subscriptions
			    SET events_since_subscription_start = GREATEST(events_since_subscription_start, $2),
			        updated_at = now()
			  WHERE id = $1`,
			sub.ID, row.EventNumber,
		); err != nil {
			return fmt.Errorf("advance cursor: %w", err)
		}

	case ActionRescheduleTransient:
		nextAt := w.clock().Add(ComputeBackoff(w.cfg.Retry, postAttempts-1, outcome.RetryAfter, w.rng))
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'pending', attempts = $2, next_attempt_at = $3,
			        last_error = $4, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, nextAt, decision.Reason,
		); err != nil {
			return fmt.Errorf("reschedule: %w", err)
		}

	case ActionDeadLetter:
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'dead', attempts = $2, last_error = $3, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, decision.Reason,
		); err != nil {
			return fmt.Errorf("mark dead: %w", err)
		}
		subID := sub.ID
		if _, err := w.dl.Insert(ctx, tx, repos.DeadLetterRow{
			Kind:           "delivery_exhausted",
			SourceTable:    "deliveries",
			SourceID:       row.ID,
			SubscriptionID: &subID,
			Reason:         decision.Reason,
			CorrelationID:  &row.CorrelationID,
		}); err != nil {
			return fmt.Errorf("dead_letter insert: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	committed = true
	return nil
}

// requeueAsTransient is the bail-out path when the scheduler cannot
// even reach the channel: subscription disappeared, ehr_event missing,
// channel unregistered, build error. The row is moved back to
// 'pending' with attempts++ so the standard retry curve applies.
func (w *Worker) requeueAsTransient(ctx context.Context, row *repos.DeliveryRow, reason string) {
	postAttempts := row.Attempts + 1
	decision := Decision{Action: ActionRescheduleTransient, Reason: reason}
	if postAttempts >= w.cfg.Retry.MaxAttempts {
		decision = Decision{Action: ActionDeadLetter, Reason: "max_attempts_exhausted: " + reason}
	}

	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		w.logger.ErrorContext(ctx, "scheduler: begin requeue tx failed", slog.Any("err", err))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	switch decision.Action {
	case ActionRescheduleTransient:
		nextAt := w.clock().Add(ComputeBackoff(w.cfg.Retry, postAttempts-1, 0, w.rng))
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'pending', attempts = $2, next_attempt_at = $3,
			        last_error = $4, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, nextAt, reason,
		); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: requeue update failed", slog.Any("err", err))
			return
		}
	case ActionDeadLetter:
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'dead', attempts = $2, last_error = $3, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, decision.Reason,
		); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: deadletter update failed", slog.Any("err", err))
			return
		}
		subID := row.SubscriptionID
		corr := row.CorrelationID
		if _, err := w.dl.Insert(ctx, tx, repos.DeadLetterRow{
			Kind:           "delivery_exhausted",
			SourceTable:    "deliveries",
			SourceID:       row.ID,
			SubscriptionID: &subID,
			Reason:         decision.Reason,
			CorrelationID:  &corr,
		}); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: dead_letter insert failed", slog.Any("err", err))
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		w.logger.ErrorContext(ctx, "scheduler: requeue commit failed", slog.Any("err", err))
		return
	}
	committed = true
}

// requeueWithReason is the bail-out path for known reasons that require
// classification: subscription/ehr_event not found is permanent, others
// are transient (S-8.2).
func (w *Worker) requeueWithReason(ctx context.Context, row *repos.DeliveryRow, reason string) {
	postAttempts := row.Attempts + 1
	decision := ClassifyRequeueReason(reason, w.cfg.Retry, postAttempts)
	w.applyBailoutDecision(ctx, row, decision, postAttempts, reason)
}

// applyBuildDecision applies a Decision produced by ClassifyBuildError.
func (w *Worker) applyBuildDecision(ctx context.Context, row *repos.DeliveryRow, decision Decision, fullReason string) {
	postAttempts := row.Attempts + 1
	if decision.Reason != "" && fullReason != "" {
		decision.Reason = fullReason
	}
	w.applyBailoutDecision(ctx, row, decision, postAttempts, fullReason)
}

// applyBailoutDecision is the shared dead-letter / reschedule writer
// for paths where we never reached the channel.
func (w *Worker) applyBailoutDecision(ctx context.Context, row *repos.DeliveryRow, decision Decision, postAttempts int32, fallbackReason string) {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		w.logger.ErrorContext(ctx, "scheduler: begin requeue tx failed", slog.Any("err", err))
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	switch decision.Action {
	case ActionRescheduleTransient:
		nextAt := w.clock().Add(ComputeBackoff(w.cfg.Retry, postAttempts-1, 0, w.rng))
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'pending', attempts = $2, next_attempt_at = $3,
			        last_error = $4, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, nextAt, decision.Reason,
		); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: requeue update failed", slog.Any("err", err))
			return
		}
	case ActionDeadLetter:
		reason := decision.Reason
		if reason == "" {
			reason = fallbackReason
		}
		if _, err := tx.Exec(ctx,
			`UPDATE deliveries
			    SET status = 'dead', attempts = $2, last_error = $3, updated_at = now()
			  WHERE id = $1`,
			row.ID, postAttempts, reason,
		); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: deadletter update failed", slog.Any("err", err))
			return
		}
		subID := row.SubscriptionID
		corr := row.CorrelationID
		if _, err := w.dl.Insert(ctx, tx, repos.DeadLetterRow{
			Kind:           "delivery_exhausted",
			SourceTable:    "deliveries",
			SourceID:       row.ID,
			SubscriptionID: &subID,
			Reason:         reason,
			CorrelationID:  &corr,
		}); err != nil {
			w.logger.ErrorContext(ctx, "scheduler: dead_letter insert failed", slog.Any("err", err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		w.logger.ErrorContext(ctx, "scheduler: requeue commit failed", slog.Any("err", err))
		return
	}
	committed = true
}

// isPermanentBuildError matches deterministic build-time conditions
// that will never succeed on retry (S-8.3).
func isPermanentBuildError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		"subscription has nil id",
		"decode focus resource",
		"marshal status",
		"marshal bundle",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// loadEhrEvent reads one ehr_events row by id via the repo's GetByID
// (which handles codec decrypt). Returns nil for "not found" so the
// caller can decide between transient retry and dead-letter.
func (w *Worker) loadEhrEvent(ctx context.Context, id uuid.UUID) (*repos.EhrEventRow, error) {
	return w.ehr.GetByID(ctx, w.pool, id)
}

func nextBackoff(cur, ceiling time.Duration) time.Duration {
	next := cur * 2
	if next > ceiling {
		return ceiling
	}
	return next
}

// attemptsToUint32 narrows the deliveries.attempts int32 column onto
// the channel.NotificationEnvelope.Attempt uint32 field. Attempts is
// always non-negative (the schema sets it to 0 and only increments);
// negative attempts are clamped to 0 defensively. This is a
// gosec-G115-safe conversion.
func attemptsToUint32(a int32) uint32 {
	if a < 0 {
		return 0
	}
	return uint32(a)
}
