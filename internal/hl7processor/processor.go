// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Defaults per LLD §7.
const (
	DefaultClaimBatchSize        = 16
	DefaultClaimIdlePollInterval = 1 * time.Second
	DefaultReaperTickInterval    = 5 * time.Second
	// DefaultReaperBatchSize bounds how many expired pending_pairs the
	// reaper sweeps per tick. Was hardcoded inline; now a knob (S-9.6).
	DefaultReaperBatchSize = 64
	// DefaultMaxRowAttempts caps how many times a single hl7_message_queue
	// row may have its processOne BeginTx fail before the framework
	// dead-letters it (S-9.9). Mirrors the matcher (S-10.6) and submatcher
	// (S-12) defaults.
	DefaultMaxRowAttempts = 8
)

// Config tunes processor behavior. Zero values fall back to package
// defaults; the constructor performs validation and replacement.
type Config struct {
	// AdapterID is stamped onto every resource_changes row and metric
	// label. Empty is rejected at construction.
	AdapterID string

	// ClaimBatchSize bounds the number of rows pulled per claim cycle.
	// Default [DefaultClaimBatchSize].
	ClaimBatchSize int32

	// ClaimIdlePollInterval is the polling cadence when the wakeup
	// signal is silent. Default [DefaultClaimIdlePollInterval].
	ClaimIdlePollInterval time.Duration

	// ReaperTickInterval is how often the reaper sweeps pending_pairs.
	// Default [DefaultReaperTickInterval].
	ReaperTickInterval time.Duration

	// ReaperBatchSize bounds how many expired pending_pairs rows the
	// reaper processes per tick. Default [DefaultReaperBatchSize].
	// Operators with very high pair throughput want a larger value;
	// the previous inline LIMIT of 64 was hardcoded (S-9.6).
	ReaperBatchSize int32

	// CorrelationHoldWindow overrides the SPI default (30s) when non-zero.
	// Per-resource-type tuning is the SPI's job; this is the framework
	// fallback when the adapter does not specialize.
	CorrelationHoldWindow time.Duration

	// MaxRowAttempts caps how many times a single hl7_message_queue row
	// may have its processOne transaction fail at BeginTx before the
	// framework dead-letters it with reason=tx_begin_failed (S-9.9).
	// Without this knob a poison row (or one that always trips the
	// pool's BeginTx, e.g. statement_timeout on the tx-start GUC) pins
	// the worker forever: the claim loop re-peeks it, BeginTx fails,
	// the row stays processed=false, repeat.
	//
	// Default [DefaultMaxRowAttempts]. Mirrors the matcher / submatcher
	// pattern (S-10.6 / S-12).
	MaxRowAttempts int32
}

// withDefaults returns a copy of cfg with zero fields replaced by the
// package defaults.
func (c Config) withDefaults() Config {
	out := c
	if out.ClaimBatchSize <= 0 {
		out.ClaimBatchSize = DefaultClaimBatchSize
	}
	if out.ClaimIdlePollInterval <= 0 {
		out.ClaimIdlePollInterval = DefaultClaimIdlePollInterval
	}
	if out.ReaperTickInterval <= 0 {
		out.ReaperTickInterval = DefaultReaperTickInterval
	}
	if out.ReaperBatchSize <= 0 {
		out.ReaperBatchSize = DefaultReaperBatchSize
	}
	if out.MaxRowAttempts <= 0 {
		out.MaxRowAttempts = DefaultMaxRowAttempts
	}
	return out
}

// Validate reports whether cfg is usable for [New].
func (c Config) Validate() error {
	if c.AdapterID == "" {
		return errors.New("hl7processor: AdapterID is required")
	}
	return nil
}

// Deps groups the host-injected dependencies the processor cannot stand
// up itself. Pool, codec, and the four repos come from the storage
// module; the SPI implementation comes from the active EHR adapter.
type Deps struct {
	Pool       *pgxpool.Pool
	Codec      *codec.Codec
	HL7Queue   *repos.Hl7MessageQueueRepo
	Pending    *repos.PendingPairsRepo
	Changes    *repos.ResourceChangesRepo
	DeadLetter *repos.DeadLettersRepo

	Adapter spi.Hl7MessageProcessor

	// Metrics, Logger, Now, and Wakeup are optional; nil-safe defaults
	// are installed by [New].
	Metrics MetricsEmitter
	Logger  *slog.Logger
	Now     func() time.Time
	Wakeup  <-chan struct{}
}

// Processor is the HL7 Message Processor sub-component. Drive it with
// [Processor.Run]; shut down by canceling the supplied context.
type Processor struct {
	cfg  Config
	deps Deps

	// beginTx is the transactional entry point used by processOne and
	// writeDeadLetter. Defaults to deps.Pool.BeginTx; tests inject a
	// failure-injecting wrapper to drive the S-9.9 retry-budget path
	// without rolling the whole pool.
	beginTx func(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)

	wg       sync.WaitGroup
	stopOnce sync.Once
	stopped  chan struct{}
}

// New constructs a [Processor] from [Config] and [Deps]. Validates
// inputs; returns an error on misconfiguration.
func New(cfg Config, deps Deps) (*Processor, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if deps.Pool == nil {
		return nil, errors.New("hl7processor: Deps.Pool is required")
	}
	if deps.Codec == nil {
		return nil, errors.New("hl7processor: Deps.Codec is required")
	}
	if deps.HL7Queue == nil {
		return nil, errors.New("hl7processor: Deps.HL7Queue is required")
	}
	if deps.Pending == nil {
		return nil, errors.New("hl7processor: Deps.Pending is required")
	}
	if deps.Changes == nil {
		return nil, errors.New("hl7processor: Deps.Changes is required")
	}
	if deps.DeadLetter == nil {
		return nil, errors.New("hl7processor: Deps.DeadLetter is required")
	}
	if deps.Adapter == nil {
		return nil, errors.New("hl7processor: Deps.Adapter is required")
	}

	if deps.Metrics == nil {
		deps.Metrics = nopMetrics{}
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}

	p := &Processor{
		cfg:     cfg.withDefaults(),
		deps:    deps,
		stopped: make(chan struct{}),
	}
	p.beginTx = p.defaultBeginTx
	return p, nil
}

// defaultBeginTx is the production beginTx delegate. It calls the pool
// directly; tests override [Processor.beginTx] to drive failure paths.
func (p *Processor) defaultBeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return p.deps.Pool.BeginTx(ctx, opts)
}

// discardWriter is a no-op io.Writer used when the caller passes a nil
// logger; we still want a working *slog.Logger so .With / structured
// fields don't panic.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// Run drives the claim loop and the expiry reaper concurrently. It
// returns when ctx is canceled, after both goroutines have drained.
// LLD §4.1 + §4.5.
func (p *Processor) Run(ctx context.Context) error {
	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		p.runClaimLoop(ctx)
	}()
	go func() {
		defer p.wg.Done()
		p.runReaper(ctx)
	}()
	p.wg.Wait()
	p.stopOnce.Do(func() { close(p.stopped) })
	return nil
}

// Done returns a channel closed after [Processor.Run] has fully drained.
func (p *Processor) Done() <-chan struct{} { return p.stopped }

// runClaimLoop is the framework's main worker. LLD §4.1.
func (p *Processor) runClaimLoop(ctx context.Context) {
	tick := time.NewTicker(p.cfg.ClaimIdlePollInterval)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.deps.Wakeup:
		case <-tick.C:
		}

		if err := p.claimAndProcessOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			p.metrics().Inc(MetricClaimCycleErrors, map[string]string{
				"adapter_id": p.cfg.AdapterID,
				"loop":       "claim",
			})
			p.deps.Logger.ErrorContext(ctx, "hl7processor: claim cycle error",
				slog.String("component", "hl7_processor"),
				slog.String("adapter_id", p.cfg.AdapterID),
				slog.String("error", err.Error()),
			)
		}
	}
}

// claimAndProcessOnce performs one claim cycle: claim a batch under one
// transaction (the lock holds for that batch's lifetime via the per-row
// txs we open below). To honor SKIP LOCKED semantics across workers, we
// open a fresh tx per claimed row so that crashing while processing one
// row does not block the others.
func (p *Processor) claimAndProcessOnce(ctx context.Context) error {
	// Read claimed-row ids in a short tx, then process each in its own
	// tx. The two-phase shape sidesteps the "claim batch holds rows
	// locked while we work" problem; the SKIP LOCKED contract only
	// requires that a worker not block sibling workers, not that the
	// batch be processed under one lock.
	//
	// We do the actual work inside the per-row tx, where we also
	// acquire FOR UPDATE again to ensure no other worker has snuck in
	// between (race-free: the second claim returns 0 rows when another
	// worker already claimed).
	claims, err := p.peekUnprocessed(ctx)
	if err != nil {
		return fmt.Errorf("hl7processor: peek: %w", err)
	}
	if len(claims) == 0 {
		return nil
	}
	for _, id := range claims {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.processOne(ctx, id)
	}
	return nil
}

// peekUnprocessed returns the ids of up to ClaimBatchSize unprocessed
// queue rows that are NOT currently held in pending_pairs. The intent
// here is purely to fan work out across multiple process_one calls; the
// per-row tx reacquires FOR UPDATE on the row.
//
// We exclude rows referenced by pending_pairs.source_message_id because
// LLD §4.2 says held source rows stay processed=false (so a restart
// resumes them via the reaper) but we must NOT re-claim them through
// the normal claim loop or the framework will re-translate the held
// half as a same-kind pair under its own correlation key.
func (p *Processor) peekUnprocessed(ctx context.Context) ([]uuid.UUID, error) {
	tx, err := p.deps.Pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id
		FROM hl7_message_queue q
		WHERE processed = false
		  AND NOT EXISTS (
		      SELECT 1 FROM pending_pairs p WHERE p.source_message_id = q.id
		  )
		ORDER BY received_at ASC
		LIMIT $1`, p.cfg.ClaimBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]uuid.UUID, 0, p.cfg.ClaimBatchSize)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// processOne drives one queued message through the pipeline. The whole
// function runs inside one Postgres transaction so source-row UPDATE,
// resource_changes / pending_pairs / dead_letters INSERT, and any
// partner-row updates commit atomically. LLD §4.2.
func (p *Processor) processOne(ctx context.Context, id uuid.UUID) {
	start := p.deps.Now()
	tx, err := p.beginTx(ctx, pgx.TxOptions{})
	if err != nil {
		p.handleBeginTxFailure(ctx, id, err)
		return
	}
	defer func() {
		// Defensive — Run() commits or Rollback()s explicitly; this is the
		// catch-all for early returns.
		_ = tx.Rollback(ctx)
	}()

	row, ok, err := p.lockRow(ctx, tx, id)
	if err != nil {
		_ = tx.Rollback(ctx)
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeRolledBack})
		p.deps.Logger.ErrorContext(ctx, "hl7processor: lock row",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", id.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if !ok {
		// Lost the race — another worker grabbed the row first. No-op.
		_ = tx.Rollback(ctx)
		return
	}

	outcome, terr := p.decideOutcome(ctx, tx, row)
	if terr != nil {
		// Rollback intermediate state and start a fresh tx for the
		// dead-letter write so we do not leave the input row processed
		// without a recorded artifact.
		_ = tx.Rollback(ctx)
		p.writeDeadLetter(ctx, row, terr)
		return
	}

	if err := p.applyOutcome(ctx, tx, row, outcome); err != nil {
		_ = tx.Rollback(ctx)
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeRolledBack})
		p.deps.Logger.ErrorContext(ctx, "hl7processor: apply outcome",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", id.String()),
			slog.String("error", err.Error()),
		)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeRolledBack})
		p.deps.Logger.ErrorContext(ctx, "hl7processor: commit",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", id.String()),
			slog.String("error", err.Error()),
		)
		return
	}

	dur := p.deps.Now().Sub(start).Seconds()
	p.metrics().Observe(MetricStageDurationSeconds, dur, map[string]string{"stage": "translate"})
	p.metrics().Observe(MetricProcessingDuration, dur, map[string]string{"outcome": outcomeLabel(outcome.kind)})
}

// handleBeginTxFailure is the per-row retry-budget enforcement for the
// processOne BeginTx failure path (S-9.9). Without it, a row whose
// tx-begin always fails (db blip, pool exhaustion, statement_timeout on
// a session GUC) stays processed=false forever — the claim loop
// re-peeks it on the next tick and the same failure repeats, pinning
// the worker.
//
// We bump hl7_message_queue.attempt_count via a fresh, short-lived
// statement on the pool — *not* the failed transaction — so a transient
// pool error on BeginTx does not also block the increment. When the
// new attempt_count exceeds the configured cap, the row is dead-lettered
// with reason=tx_begin_failed; otherwise the loop falls through and the
// next claim cycle retries.
func (p *Processor) handleBeginTxFailure(ctx context.Context, id uuid.UUID, beginErr error) {
	p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeTxBeginFailed})
	p.metrics().Inc(MetricHL7TxBeginFailed, map[string]string{"adapter_id": p.cfg.AdapterID})

	p.deps.Logger.ErrorContext(ctx, "hl7processor: begin tx",
		slog.String("component", "hl7_processor"),
		slog.String("adapter_id", p.cfg.AdapterID),
		slog.String("source_message_id", id.String()),
		slog.String("error", beginErr.Error()),
	)

	attempts, err := p.deps.HL7Queue.IncrementAttemptCount(ctx, p.deps.Pool, id)
	if err != nil {
		// The DB is too sick to even bump the counter. The row stays
		// processed=false; the next tick retries. We log so operators
		// see compounded failure but do not dead-letter — a Postgres
		// outage must not silently dead-letter the inbound queue.
		p.deps.Logger.ErrorContext(ctx, "hl7processor: increment attempt_count after begin tx failure",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", id.String()),
			slog.String("error", err.Error()),
		)
		return
	}

	if attempts <= p.cfg.MaxRowAttempts {
		// Budget remains; transient failure, let the next claim cycle
		// retry the row.
		return
	}

	// Budget exhausted — dead-letter so the row leaves the unprocessed
	// queue and an operator artifact lands in dead_letters.
	row, getErr := p.deps.HL7Queue.GetByID(ctx, p.deps.Pool, id)
	if getErr != nil || row == nil {
		// Same DB-too-sick logic as above; log and let the next tick try.
		if getErr != nil {
			p.deps.Logger.ErrorContext(ctx, "hl7processor: lookup row for tx_begin_failed dead-letter",
				slog.String("component", "hl7_processor"),
				slog.String("adapter_id", p.cfg.AdapterID),
				slog.String("source_message_id", id.String()),
				slog.String("error", getErr.Error()),
			)
		}
		return
	}

	terr := &translateError{
		Class: ErrorClassTxBeginFailed,
		Err:   fmt.Errorf("tx_begin_failed after %d attempts: %w", attempts, beginErr),
	}
	p.writeDeadLetter(ctx, *row, terr)
}

// lockRow acquires SELECT FOR UPDATE on the queue row and returns its
// decoded form. The bool is false when another worker already processed
// or claimed the row.
func (p *Processor) lockRow(ctx context.Context, tx pgx.Tx, id uuid.UUID) (repos.Hl7MessageQueueRow, bool, error) {
	const sql = `
		SELECT id, listener_endpoint, peer_addr, received_at, mllp_message_id,
		       correlation_id, raw_body, key_version, processed
		FROM hl7_message_queue
		WHERE id = $1 AND processed = false
		FOR UPDATE SKIP LOCKED`
	var row repos.Hl7MessageQueueRow
	var enc []byte
	err := tx.QueryRow(ctx, sql, id).Scan(
		&row.ID, &row.ListenerEndpoint, &row.PeerAddr, &row.ReceivedAt,
		&row.MllpMessageID, &row.CorrelationID, &enc, &row.KeyVersion, &row.Processed,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return repos.Hl7MessageQueueRow{}, false, nil
	}
	if err != nil {
		return repos.Hl7MessageQueueRow{}, false, err
	}
	body, err := p.deps.Codec.Decrypt(enc, row.KeyVersion, repos.AADHl7MessageQueue(row.ID, row.KeyVersion))
	if err != nil {
		return repos.Hl7MessageQueueRow{}, false, fmt.Errorf("decrypt body: %w", err)
	}
	row.RawBody = body
	return row, true, nil
}

// decideOutcome runs translate, then applies the cancel-and-replace
// state machine to decide what gets written. Returns a [*translateError]
// when a terminal failure should dead-letter.
func (p *Processor) decideOutcome(ctx context.Context, tx pgx.Tx, row repos.Hl7MessageQueueRow) (processingOutcome, error) {
	tr, err := translate(p.deps.Adapter, row.RawBody, "")
	if err != nil {
		// Allow already-classed translateError to surface unchanged;
		// otherwise wrap as Unexpected.
		if asTranslateError(err) != nil {
			return processingOutcome{}, err
		}
		return processingOutcome{}, &translateError{Class: ErrorClassUnexpected, Err: err}
	}
	resourceType := tr.resource.ResourceType
	ext := deriveClassifyExt(tr.classify, resourceType)

	// S-9.10: prefer MSH-7 (sender's stamped time) when the parsed
	// message carries it. The vendor ParsedHL7Message can expose it
	// via the optional MessageDateTime field; if absent or unparseable
	// we fall back to "now()".
	occurred := p.deps.Now()
	if t, ok := messageDateTime(tr.parsed); ok {
		occurred = t
	}
	if ext.CorrelationKey == "" {
		return processingOutcome{
			kind:    outcomeEmitted,
			emitted: buildPlainChange(ext, tr.resource, row.CorrelationID, occurred),
		}, nil
	}

	// Pairing path: look up pending_pairs WHERE correlation_key+endpoint
	// FOR UPDATE so concurrent partner arrivals serialize on this row.
	existing, ok, err := p.lockPending(ctx, tx, ext.CorrelationKey, row.ListenerEndpoint)
	if err != nil {
		return processingOutcome{}, &translateError{Class: ErrorClassUnexpected, Err: fmt.Errorf("lock pending: %w", err)}
	}

	if !ok {
		// Nothing held under this key. Hold the cancellation half;
		// emit replacements plain.
		if ext.IsCancellationHalf {
			window := p.deps.Adapter.CorrelationHoldWindow()
			if p.cfg.CorrelationHoldWindow > 0 {
				window = p.cfg.CorrelationHoldWindow
			}
			return processingOutcome{
				kind: outcomeHeld,
				held: heldPair{
					CorrelationKey:   ext.CorrelationKey,
					ListenerEndpoint: row.ListenerEndpoint,
					Resource:         tr.resource,
					PendingKind:      spi.ChangeDelete,
					SourceMessageID:  row.ID,
					ExpiresAt:        occurred.Add(window),
					CreatedAt:        occurred,
					ResourceType:     ext.ResourceType,
					CorrelationID:    row.CorrelationID,
				},
			}, nil
		}
		// Replacement-with-no-cancellation. ADR 0008 #7 says hold
		// replacements symmetrically, but the existing schema's
		// pending_kind only allows 'delete'/'create'. We still hold by
		// kind=create.
		//
		// N-1: the `create` enum value here is overloaded — it means
		// either "first half of a fresh create-pair" OR "held
		// replacement awaiting its cancellation half." Disambiguate
		// downstream by inspecting the source HL7 control segment if
		// you need to tell them apart; renaming the enum to add a
		// dedicated `replacement_held` value is a schema migration
		// outside N-1 polish scope.
		if ext.IsReplacementHalf {
			window := p.deps.Adapter.CorrelationHoldWindow()
			if p.cfg.CorrelationHoldWindow > 0 {
				window = p.cfg.CorrelationHoldWindow
			}
			return processingOutcome{
				kind: outcomeHeld,
				held: heldPair{
					CorrelationKey:   ext.CorrelationKey,
					ListenerEndpoint: row.ListenerEndpoint,
					Resource:         tr.resource,
					PendingKind:      spi.ChangeCreate,
					SourceMessageID:  row.ID,
					ExpiresAt:        occurred.Add(window),
					CreatedAt:        occurred,
					ResourceType:     ext.ResourceType,
					CorrelationID:    row.CorrelationID,
				},
			}, nil
		}
		return processingOutcome{
			kind:    outcomeEmitted,
			emitted: buildPlainChange(ext, tr.resource, row.CorrelationID, occurred),
		}, nil
	}

	// Existing pending row found. Try to merge.
	heldKind := pendingKindToChange(existing.PendingKind)
	merged, mergeErr := mergePair(
		heldKind, existing.HeldResource,
		ext.Kind, tr.resource,
		ext.ResourceType, existing.HeldCorrelationID,
		existing.CreatedAt, occurred,
	)
	if mergeErr != nil {
		// Same-kind defensive case — emit the arriving message plain;
		// leave the held row alone for the reaper.
		p.metrics().Inc(MetricSameKindCollision, map[string]string{
			"adapter_id":    p.cfg.AdapterID,
			"resource_type": ext.ResourceType,
			"held_kind":     string(existing.PendingKind),
			"arriving_kind": string(ext.Kind),
		})
		p.deps.Logger.ErrorContext(ctx, "hl7processor: same-kind pair under correlation key",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("correlation_key", existing.CorrelationKey),
			slog.String("listener_endpoint", existing.ListenerEndpoint),
			slog.String("held_kind", string(existing.PendingKind)),
			slog.String("arriving_kind", string(ext.Kind)),
		)
		return processingOutcome{
			kind:    outcomeEmitted,
			emitted: buildPlainChange(ext, tr.resource, row.CorrelationID, occurred),
		}, nil
	}
	return processingOutcome{
		kind: outcomeResolved,
		resolved: resolvedPair{
			Merged:                merged,
			PartnerSourceID:       existing.SourceMessageID,
			ClearCorrelationKey:   existing.CorrelationKey,
			ClearListenerEndpoint: existing.ListenerEndpoint,
		},
	}, nil
}

// pendingFull is the in-memory shape returned from [Processor.lockPending].
// It bundles the decoded pending row plus the held FhirResource.
type pendingFull struct {
	repos.PendingPairRow
	HeldResource      spi.FhirResource
	HeldCorrelationID uuid.UUID
}

// lockPending takes FOR UPDATE on the pending_pairs row keyed by
// (correlation_key, listener_endpoint). The bool is false when no row
// exists. The encrypted pending_resource is decrypted and the held
// half's correlation_id is fetched from hl7_message_queue.
func (p *Processor) lockPending(ctx context.Context, tx pgx.Tx, key, endpoint string) (pendingFull, bool, error) {
	const sql = `
		SELECT correlation_key, listener_endpoint, pending_resource, pending_kind,
		       source_message_id, expires_at, created_at, key_version
		FROM pending_pairs
		WHERE correlation_key = $1 AND listener_endpoint = $2
		FOR UPDATE`
	var row repos.PendingPairRow
	var enc []byte
	var kind string
	err := tx.QueryRow(ctx, sql, key, endpoint).Scan(
		&row.CorrelationKey, &row.ListenerEndpoint, &enc, &kind,
		&row.SourceMessageID, &row.ExpiresAt, &row.CreatedAt, &row.KeyVersion,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return pendingFull{}, false, nil
	}
	if err != nil {
		return pendingFull{}, false, err
	}
	row.PendingKind = repos.PendingKind(kind)

	// Decrypt with the row's persisted key_version so a key rotation
	// after the half-pair was held still resolves to the correct cipher.
	body, err := p.deps.Codec.Decrypt(enc, row.KeyVersion,
		repos.AADPendingPairs(row.CorrelationKey, row.ListenerEndpoint, row.KeyVersion))
	if err != nil {
		return pendingFull{}, false, fmt.Errorf("decrypt pending: %w", err)
	}
	resource, err := decodePendingResource(body)
	if err != nil {
		return pendingFull{}, false, fmt.Errorf("decode pending: %w", err)
	}

	// Pull the held half's correlation_id from hl7_message_queue. We
	// stamp it on the merged ResourceChange so retries are idempotent.
	heldCorr, err := lookupCorrelationID(ctx, tx, row.SourceMessageID)
	if err != nil {
		return pendingFull{}, false, fmt.Errorf("lookup held correlation_id: %w", err)
	}
	return pendingFull{
		PendingPairRow:    row,
		HeldResource:      resource,
		HeldCorrelationID: heldCorr,
	}, true, nil
}

// lookupCorrelationID returns the correlation_id of the queue row id.
func lookupCorrelationID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (uuid.UUID, error) {
	var corr uuid.UUID
	err := tx.QueryRow(ctx, `SELECT correlation_id FROM hl7_message_queue WHERE id = $1`, id).Scan(&corr)
	if err != nil {
		return uuid.Nil, err
	}
	return corr, nil
}

// applyOutcome writes the outcome to the database. All writes go through
// the open tx so they commit atomically.
func (p *Processor) applyOutcome(ctx context.Context, tx pgx.Tx, row repos.Hl7MessageQueueRow, o processingOutcome) error {
	switch o.kind {
	case outcomeEmitted:
		if _, err := p.deps.HL7Queue.MarkProcessed(ctx, tx, row.ID); err != nil {
			return err
		}
		if err := p.insertResourceChange(ctx, tx, o.emitted); err != nil {
			return err
		}
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeEmitted})
		p.metrics().Inc(MetricResourceChangesTotal, map[string]string{
			"adapter_id":    p.cfg.AdapterID,
			"change_kind":   string(o.emitted.ChangeKind),
			"resource_type": o.emitted.ResourceType,
		})
		return nil

	case outcomeHeld:
		body, err := encodePendingResource(o.held.Resource)
		if err != nil {
			return err
		}
		if err := p.deps.Pending.Insert(ctx, tx, repos.PendingPairRow{
			CorrelationKey:   o.held.CorrelationKey,
			ListenerEndpoint: o.held.ListenerEndpoint,
			PendingResource:  body,
			PendingKind:      pendingKindFromChange(o.held.PendingKind),
			SourceMessageID:  o.held.SourceMessageID,
			ExpiresAt:        o.held.ExpiresAt,
			CreatedAt:        o.held.CreatedAt,
		}); err != nil {
			return err
		}
		// Source row stays processed=false intentionally (LLD §4.2).
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeHeld})
		p.metrics().Inc(MetricPairsHeld, map[string]string{"resource_type": o.held.ResourceType})
		return nil

	case outcomeResolved:
		if _, err := p.deps.HL7Queue.MarkProcessed(ctx, tx, row.ID); err != nil {
			return err
		}
		if _, err := p.deps.HL7Queue.MarkProcessed(ctx, tx, o.resolved.PartnerSourceID); err != nil {
			return err
		}
		if err := p.deps.Pending.Delete(ctx, tx, o.resolved.ClearCorrelationKey, o.resolved.ClearListenerEndpoint); err != nil {
			return err
		}
		if err := p.insertResourceChange(ctx, tx, o.resolved.Merged); err != nil {
			return err
		}
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeResolved})
		p.metrics().Inc(MetricPairsResolved, map[string]string{"resource_type": o.resolved.Merged.ResourceType})
		p.metrics().Inc(MetricResourceChangesTotal, map[string]string{
			"adapter_id":    p.cfg.AdapterID,
			"change_kind":   string(o.resolved.Merged.ChangeKind),
			"resource_type": o.resolved.Merged.ResourceType,
		})
		return nil

	default:
		return fmt.Errorf("hl7processor: invalid outcome kind %d", o.kind)
	}
}

// insertResourceChange writes one resource_changes row for the given
// change. The repos layer encrypts and partitions; we marshal the
// previous_resource bytes here.
func (p *Processor) insertResourceChange(ctx context.Context, tx pgx.Tx, ch spi.ResourceChange) error {
	row := repos.ResourceChangeRow{
		AdapterID:     p.cfg.AdapterID,
		CorrelationID: ch.CorrelationID,
		ResourceType:  ch.ResourceType,
		ChangeKind:    repos.ChangeKind(ch.ChangeKind),
		Resource:      ch.Resource.Body,
		OccurredAt:    ch.OccurredAt,
		EventCode:     ch.EventCode,
	}
	if ch.PreviousResource != nil {
		row.PreviousResource = ch.PreviousResource.Body
	}
	_, _, err := p.deps.Changes.Insert(ctx, tx, row)
	return err
}

// writeDeadLetter records the terminal failure on its own transaction so
// the source row leaves a downstream artifact. LLD §4.6.
func (p *Processor) writeDeadLetter(ctx context.Context, row repos.Hl7MessageQueueRow, terr error) {
	te := asTranslateError(terr)
	class := ErrorClassUnexpected
	reason := terr.Error()
	if te != nil {
		class = te.Class
		if te.Err != nil {
			reason = te.Err.Error()
		}
	}

	tx, err := p.deps.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeRolledBack})
		p.deps.Logger.ErrorContext(ctx, "hl7processor: dead-letter begin tx",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", row.ID.String()),
			slog.String("error_class", class.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := p.deps.HL7Queue.MarkProcessed(ctx, tx, row.ID); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: dead-letter mark processed",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", row.ID.String()),
			slog.String("error_class", class.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	dlKind := dlKindForClass(class)
	corrCopy := row.CorrelationID
	detail, _ := json.Marshal(map[string]string{
		"error_class":       class.String(),
		"reason":            reason,
		"listener_endpoint": row.ListenerEndpoint,
		"mllp_message_id":   row.MllpMessageID,
	})
	if _, err := p.deps.DeadLetter.Insert(ctx, tx, repos.DeadLetterRow{
		Kind:            dlKind,
		SourceTable:     "hl7_message_queue",
		SourceID:        row.ID,
		Reason:          reason,
		ErrorDetail:     detail,
		PayloadRedacted: row.RawBody,
		CorrelationID:   &corrCopy,
	}); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: dead-letter insert",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", row.ID.String()),
			slog.String("error_class", class.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		p.deps.Logger.ErrorContext(ctx, "hl7processor: dead-letter commit",
			slog.String("component", "hl7_processor"),
			slog.String("adapter_id", p.cfg.AdapterID),
			slog.String("source_message_id", row.ID.String()),
			slog.String("error_class", class.String()),
			slog.String("error", err.Error()),
		)
		return
	}
	p.metrics().Inc(MetricMessagesProcessed, map[string]string{"outcome": OutcomeDeadLetter})
	p.metrics().Inc(MetricDeadLetteredTotal, map[string]string{"error_class": class.String()})
	p.metrics().Inc(MetricDeadLettersTotal, map[string]string{
		"source": "hl7_translation",
		"reason": class.String(),
	})
}

// dlKindForClass maps an [ErrorClass] to the dead_letters.kind enum.
func dlKindForClass(c ErrorClass) string {
	switch c {
	case ErrorClassValidation:
		return "hl7_invalid_fhir"
	default:
		return "hl7_unparseable"
	}
}

// pendingKindFromChange maps SPI ChangeKind to the pending_pairs.kind
// enum. Only Delete and Create are valid pending kinds.
func pendingKindFromChange(c spi.ChangeKind) repos.PendingKind {
	if c == spi.ChangeCreate {
		return repos.PendingCreate
	}
	return repos.PendingDelete
}

// pendingKindToChange is the inverse.
func pendingKindToChange(p repos.PendingKind) spi.ChangeKind {
	if p == repos.PendingCreate {
		return spi.ChangeCreate
	}
	return spi.ChangeDelete
}

// outcomeLabel maps an [outcomeKind] to its metric label.
func outcomeLabel(k outcomeKind) string {
	switch k {
	case outcomeEmitted:
		return OutcomeEmitted
	case outcomeHeld:
		return OutcomeHeld
	case outcomeResolved:
		return OutcomeResolved
	case outcomeDeadLetter:
		return OutcomeDeadLetter
	default:
		return "unknown"
	}
}

// metrics returns a non-nil emitter (nopMetrics if none configured).
func (p *Processor) metrics() MetricsEmitter {
	if p.deps.Metrics == nil {
		return nopMetrics{}
	}
	return p.deps.Metrics
}

// messageDateTime parses MSH-7 (sender's date/time) from the parsed
// message Raw bytes and returns the resulting time.Time. Returns (zero,
// false) when MSH-7 is absent or unparseable. Used by processOne to
// source `occurred` from the EHR's stamp rather than the framework's
// wall clock (S-9.10).
//
// Robustness contracts (OP #194/#195/#196):
//   - MSH segment id is matched case-insensitively. Vendor dialects
//     (Allscripts pre-2014, MEDITECH MAGIC) emit lowercase "msh".
//   - The field walker honors MSH-2's escape character: a byte
//     following the escape char is skipped, so escaped separators
//     inside a field do not drift the field count.
//   - Timestamp parsing accepts sub-second fractions (".SSS") and
//     timezone offsets ("+ZZZZ" / "-ZZZZ"), normalized to UTC.
func messageDateTime(parsed spi.ParsedHL7Message) (time.Time, bool) {
	if len(parsed.Raw) == 0 {
		return time.Time{}, false
	}
	// First segment up to 0x0D.
	first := parsed.Raw
	for i, b := range parsed.Raw {
		if b == 0x0D {
			first = parsed.Raw[:i]
			break
		}
	}
	if len(first) < 4 {
		return time.Time{}, false
	}
	if !(first[0] == 'M' || first[0] == 'm') ||
		!(first[1] == 'S' || first[1] == 's') ||
		!(first[2] == 'H' || first[2] == 'h') {
		return time.Time{}, false
	}
	sep := first[3]
	// MSH-2 carries component, repetition, escape, subcomponent in that
	// order. Default escape is '\'. We need only the escape byte for
	// field walking; if MSH is too short, fall back to the default.
	esc := byte('\\')
	if len(first) >= 8 {
		esc = first[6]
	}
	rest := first[3:]
	field := 0
	start := 0
	var msh7 string
	for i := 0; i < len(rest); i++ {
		// Honor MSH-2 escape: the escape char and the byte that
		// follows it are opaque field content. Skip both.
		if rest[i] == esc && i+1 < len(rest) {
			i++
			continue
		}
		if rest[i] == sep {
			if field == 6 {
				msh7 = string(rest[start:i])
				break
			}
			field++
			start = i + 1
		}
	}
	if msh7 == "" {
		return time.Time{}, false
	}
	return parseHL7Timestamp(msh7)
}

// parseHL7Timestamp attempts every supported HL7 v2 TS layout in
// most-precise-first order. Go's time.Parse rejects trailing data, so a
// fractional+offset string will not silently match a shorter prefix
// layout — but the explicit ordering makes the contract obvious.
func parseHL7Timestamp(ts string) (time.Time, bool) {
	layouts := []string{
		// Sub-second + offset (most common real-world Epic/Cerner shape).
		"20060102150405.000-0700",
		"20060102150405.00-0700",
		"20060102150405.0-0700",
		// Sub-second alone.
		"20060102150405.000",
		"20060102150405.00",
		"20060102150405.0",
		// Whole-second + offset.
		"20060102150405-0700",
		"200601021504-0700",
		// Whole-second / shorter prefixes.
		"20060102150405",
		"200601021504",
		"2006010215",
		"20060102",
		"200601",
		"2006",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
