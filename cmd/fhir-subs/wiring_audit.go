// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// pgAuditStore is a thin observability/audit.Store implementation that
// writes to the existing audit_log table (column shape:
// canonical_form / hash / prev_hash). The shipped audit.PgStore writes
// a different column layout (chain_input / chain_hash / prior_hash)
// that targets a fresh table; the API path landed in story #49 already
// owns the operator-facing schema, so this adapter keeps both writers
// pointed at the same table and the same hash chain.
//
// AcquireChainLock opens a transaction and takes the documented audit
// chain advisory lock (xact-scoped, so a panic / lost connection
// auto-releases). LastChainHash + InsertAuditRow run inside that
// transaction so the verify→insert pair is atomic. IterateRows is
// implemented for the audit verifier CLI but the production wiring
// does not exercise it; both sides target the same table.
type pgAuditStore struct {
	pool *pgxpool.Pool

	txMu     sync.Mutex
	activeTx pgx.Tx
}

// newPgAuditStore wraps pool. pool must be non-nil; production wiring
// supplies the same pool every other store uses.
func newPgAuditStore(pool *pgxpool.Pool) (*pgAuditStore, error) {
	if pool == nil {
		return nil, errors.New("audit: pool is required")
	}
	return &pgAuditStore{pool: pool}, nil
}

// AcquireChainLock begins a transaction and takes the audit chain
// advisory lock. The returned release commits the transaction, which
// auto-releases the xact-scoped lock.
func (s *pgAuditStore) AcquireChainLock(ctx context.Context) (func() error, error) {
	s.txMu.Lock()
	if s.activeTx != nil {
		s.txMu.Unlock()
		return nil, errors.New("audit: chain lock already held; concurrent Acquire is a contract violation")
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		s.txMu.Unlock()
		return nil, fmt.Errorf("audit: begin tx: %w", err)
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", audit.AuditChainAdvisoryLockID); err != nil {
		_ = tx.Rollback(ctx)
		s.txMu.Unlock()
		return nil, fmt.Errorf("audit: advisory lock: %w", err)
	}
	s.activeTx = tx
	s.txMu.Unlock()

	released := false
	return func() error {
		s.txMu.Lock()
		defer s.txMu.Unlock()
		if released {
			return nil
		}
		released = true
		active := s.activeTx
		s.activeTx = nil
		if active == nil {
			return nil
		}
		if err := active.Commit(ctx); err != nil {
			_ = active.Rollback(ctx)
			return fmt.Errorf("audit: commit: %w", err)
		}
		return nil
	}, nil
}

// LastChainHash reads the most recent row's hash. Runs under the
// active chain transaction when one is held.
func (s *pgAuditStore) LastChainHash(ctx context.Context) ([]byte, error) {
	s.txMu.Lock()
	tx := s.activeTx
	s.txMu.Unlock()

	const sql = "SELECT hash FROM audit_log ORDER BY seq DESC LIMIT 1"
	var h []byte
	var row pgx.Row
	if tx != nil {
		row = tx.QueryRow(ctx, sql)
	} else {
		row = s.pool.QueryRow(ctx, sql)
	}
	err := row.Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return h, err
}

// InsertAuditRow translates the observability/audit.Row shape into the
// existing audit_log column layout (hash / prev_hash / canonical_form)
// and inserts under the active chain transaction.
func (s *pgAuditStore) InsertAuditRow(ctx context.Context, row audit.Row) error {
	s.txMu.Lock()
	tx := s.activeTx
	s.txMu.Unlock()

	// audit_log.actor_kind has a CHECK constraint; coerce empty/unknown
	// values to "system" so the API audit (story #49) writers and the
	// observability writer both round-trip the table cleanly.
	actorKind := row.ActorKind
	switch actorKind {
	case "subscriber", "operator", "system":
	default:
		actorKind = "system"
	}
	outcome := row.Outcome
	switch outcome {
	case "success", "failure", "denied":
	default:
		outcome = "success"
	}

	var cid *uuid.UUID
	if row.CorrelationID != (uuid.UUID{}) {
		c := row.CorrelationID
		cid = &c
	}

	const sql = `
		INSERT INTO audit_log
			(actor_kind, actor_id, action, target_kind, target_id, outcome,
			 correlation_id, canonical_form, hash, prev_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`
	args := []any{
		actorKind, row.ActorID, row.Action, row.TargetKind, row.TargetID,
		outcome, cid, row.ChainInput, row.ChainHash, row.PriorHash,
	}
	if tx != nil {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	}
	_, err := s.pool.Exec(ctx, sql, args...)
	return err
}

// IterateRows visits rows in seq order. Used by the audit verifier
// CLI; the production wiring registers it but does not call it on the
// hot path.
func (s *pgAuditStore) IterateRows(ctx context.Context, fn func(audit.Row) error) error {
	const sql = `
		SELECT occurred_at, actor_kind, COALESCE(actor_id, ''), action,
		       COALESCE(target_kind, ''), COALESCE(target_id, ''), outcome,
		       correlation_id, canonical_form, hash, prev_hash
		  FROM audit_log
		 ORDER BY seq ASC`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			r        audit.Row
			cid      *uuid.UUID
			prevHash []byte
		)
		if err := rows.Scan(
			&r.OccurredAt,
			&r.ActorKind,
			&r.ActorID,
			&r.Action,
			&r.TargetKind,
			&r.TargetID,
			&r.Outcome,
			&cid,
			&r.ChainInput,
			&r.ChainHash,
			&prevHash,
		); err != nil {
			return err
		}
		if cid != nil {
			r.CorrelationID = *cid
		}
		r.PriorHash = prevHash
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// observabilityAuditAdapter satisfies handlers.AuditStore by translating
// each Append call into an observability.AuditEvent and forwarding it
// through the hash-chained audit.Writer (story #94 AC #5).
type observabilityAuditAdapter struct {
	w   observability.AuditWriter
	now func() time.Time
}

func newObservabilityAuditAdapter(w observability.AuditWriter, now func() time.Time) *observabilityAuditAdapter {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &observabilityAuditAdapter{w: w, now: now}
}

// Append translates the API's per-action shape into an audit event and
// forwards through the hash-chained writer. The canonical request body
// is stashed under payload.canonical so the durable row keeps the
// pre-hash bytes (the hash is computed over the JCS-canonicalized
// payload by the audit.Writer).
func (a *observabilityAuditAdapter) Append(
	ctx context.Context,
	action, target, outcome string,
	correlationID *uuid.UUID,
	canonical []byte,
) error {
	if a == nil || a.w == nil {
		return errors.New("audit: writer not configured")
	}
	cid := uuid.UUID{}
	if correlationID != nil {
		cid = *correlationID
	}
	evt := observability.AuditEvent{
		OccurredAt:    a.now(),
		ActorKind:     "subscriber",
		Action:        action,
		TargetKind:    "Subscription",
		TargetID:      target,
		Outcome:       outcome,
		CorrelationID: cid,
		Payload: map[string]any{
			"canonical": string(canonical),
		},
	}
	return a.w.Emit(ctx, evt)
}

// Compile-time guarantees the adapters satisfy the contracts.
var (
	_ audit.Store         = (*pgAuditStore)(nil)
	_ handlers.AuditStore = (*observabilityAuditAdapter)(nil)
)
