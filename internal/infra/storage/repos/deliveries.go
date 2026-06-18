// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DeliveriesRepo wraps the deliveries table.
type DeliveriesRepo struct{}

// NewDeliveriesRepo constructs the repo.
func NewDeliveriesRepo() *DeliveriesRepo { return &DeliveriesRepo{} }

// Insert creates a new delivery row. Returns the assigned id. Duplicate
// (subscription_id, event_number) is a no-op (ON CONFLICT DO NOTHING)
// and returns the existing id.
func (r *DeliveriesRepo) Insert(ctx context.Context, q Querier, row DeliveryRow) (uuid.UUID, error) {
	const sql = `
		INSERT INTO deliveries
			(subscription_id, ehr_event_id, event_number, status,
			 attempts, next_attempt_at, correlation_id, key_version)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (subscription_id, event_number) DO UPDATE
			SET updated_at = now()
		RETURNING id`
	status := string(row.Status)
	if status == "" {
		status = string(DeliveryPending)
	}
	kv := row.KeyVersion
	if kv == 0 {
		kv = 1
	}
	var id uuid.UUID
	if err := q.QueryRow(ctx, sql,
		row.SubscriptionID, row.EhrEventID, row.EventNumber, status,
		row.Attempts, row.NextAttemptAt, row.CorrelationID, kv,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("deliveries: insert: %w", err)
	}
	return id, nil
}

// ClaimPending claims up to limit pending rows whose next_attempt_at is
// at or before now. Sets status='delivering' on each.
//
// N-1: ORDER BY adds id ASC as a tiebreaker so concurrent workers
// observe a deterministic claim order when many rows share the same
// next_attempt_at (typical for a freshly-fanned-out batch). Without the
// tiebreaker, Postgres can return rows in any order, which complicates
// reproducing failures and reasoning about head-of-line behavior.
func (r *DeliveriesRepo) ClaimPending(ctx context.Context, tx pgx.Tx, limit int32, now time.Time) ([]DeliveryRow, error) {
	const sql = `
		SELECT id, subscription_id, ehr_event_id, event_number, status,
		       attempts, next_attempt_at, COALESCE(last_error, ''),
		       key_version, correlation_id, created_at, updated_at
		FROM deliveries
		WHERE status = 'pending' AND next_attempt_at <= $1
		ORDER BY next_attempt_at ASC, id ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, sql, now, limit)
	if err != nil {
		return nil, fmt.Errorf("deliveries: claim: %w", err)
	}
	out := make([]DeliveryRow, 0, 8)
	ids := make([]uuid.UUID, 0, 8)
	for rows.Next() {
		var rec DeliveryRow
		var status string
		if err := rows.Scan(
			&rec.ID, &rec.SubscriptionID, &rec.EhrEventID, &rec.EventNumber,
			&status, &rec.Attempts, &rec.NextAttemptAt, &rec.LastError,
			&rec.KeyVersion, &rec.CorrelationID, &rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			rows.Close()
			return nil, fmt.Errorf("deliveries: scan: %w", err)
		}
		rec.Status = DeliveryStatus(status)
		out = append(out, rec)
		ids = append(ids, rec.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("deliveries: rows: %w", err)
	}
	if len(ids) == 0 {
		return out, nil
	}
	const upd = `UPDATE deliveries SET status = 'delivering', updated_at = now() WHERE id = ANY($1)`
	if _, err := tx.Exec(ctx, upd, ids); err != nil {
		return nil, fmt.Errorf("deliveries: flip-to-delivering: %w", err)
	}
	return out, nil
}

// MarkDelivered transitions a row to delivered, recording the post-attempt
// count and clearing last_error. The scheduler calls this from the
// ActionMarkDelivered branch after a successful channel.Deliver (S-8.6).
func (r *DeliveriesRepo) MarkDelivered(ctx context.Context, q Querier, id uuid.UUID, attempts int32) error {
	const sql = `
		UPDATE deliveries
		   SET status = 'delivered', attempts = $2, last_error = NULL, updated_at = now()
		 WHERE id = $1`
	if _, err := q.Exec(ctx, sql, id, attempts); err != nil {
		return fmt.Errorf("deliveries: mark delivered: %w", err)
	}
	return nil
}

// MarkPending requeues a row for a future retry. The scheduler calls this
// from the ActionRescheduleTransient branch — both the post-channel
// outcome path and the bail-out paths (S-8.6).
func (r *DeliveriesRepo) MarkPending(ctx context.Context, q Querier, id uuid.UUID, attempts int32, nextAttemptAt time.Time, reason string) error {
	const sql = `
		UPDATE deliveries
		   SET status = 'pending', attempts = $2, next_attempt_at = $3,
		       last_error = $4, updated_at = now()
		 WHERE id = $1`
	if _, err := q.Exec(ctx, sql, id, attempts, nextAttemptAt, reason); err != nil {
		return fmt.Errorf("deliveries: mark pending: %w", err)
	}
	return nil
}

// MarkDead transitions a row to the terminal 'dead' status. The scheduler
// calls this from the ActionDeadLetter branch; the dead_letters insert is
// performed separately under the same transaction (S-8.6).
func (r *DeliveriesRepo) MarkDead(ctx context.Context, q Querier, id uuid.UUID, attempts int32, reason string) error {
	const sql = `
		UPDATE deliveries
		   SET status = 'dead', attempts = $2, last_error = $3, updated_at = now()
		 WHERE id = $1`
	if _, err := q.Exec(ctx, sql, id, attempts, reason); err != nil {
		return fmt.Errorf("deliveries: mark dead: %w", err)
	}
	return nil
}

// GetByID returns one row by id, or nil.
func (r *DeliveriesRepo) GetByID(ctx context.Context, q Querier, id uuid.UUID) (*DeliveryRow, error) {
	const sql = `
		SELECT id, subscription_id, ehr_event_id, event_number, status,
		       attempts, next_attempt_at, COALESCE(last_error, ''),
		       key_version, correlation_id, created_at, updated_at
		FROM deliveries
		WHERE id = $1`
	var rec DeliveryRow
	var status string
	row := q.QueryRow(ctx, sql, id)
	if err := row.Scan(
		&rec.ID, &rec.SubscriptionID, &rec.EhrEventID, &rec.EventNumber,
		&status, &rec.Attempts, &rec.NextAttemptAt, &rec.LastError,
		&rec.KeyVersion, &rec.CorrelationID, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("deliveries: get: %w", err)
	}
	rec.Status = DeliveryStatus(status)
	return &rec, nil
}
