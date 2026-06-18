// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// Hl7MessageQueueRepo wraps the hl7_message_queue table.
type Hl7MessageQueueRepo struct {
	codec *codec.Codec
}

// NewHl7MessageQueueRepo constructs the repo.
func NewHl7MessageQueueRepo(c *codec.Codec) *Hl7MessageQueueRepo {
	return &Hl7MessageQueueRepo{codec: c}
}

// Insert persists a row. RawBody is encrypted under the codec's active
// key. Returns the generated UUID.
func (r *Hl7MessageQueueRepo) Insert(ctx context.Context, q Querier, row Hl7MessageQueueRow) (uuid.UUID, error) {
	enc, kv, err := r.codec.Encrypt(row.RawBody)
	if err != nil {
		return uuid.Nil, fmt.Errorf("hl7_message_queue: encrypt: %w", err)
	}
	const sql = `
		INSERT INTO hl7_message_queue
			(listener_endpoint, peer_addr, mllp_message_id, correlation_id, raw_body, key_version)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`
	var id uuid.UUID
	if err := q.QueryRow(ctx, sql,
		row.ListenerEndpoint, row.PeerAddr, row.MllpMessageID,
		row.CorrelationID, enc, kv,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("hl7_message_queue: insert: %w", err)
	}
	return id, nil
}

// ClaimUnprocessed pulls up to limit unprocessed rows under FOR UPDATE
// SKIP LOCKED. The transaction must remain open for the lock to hold;
// the caller is expected to mark the rows processed before commit.
func (r *Hl7MessageQueueRepo) ClaimUnprocessed(ctx context.Context, tx pgx.Tx, limit int32) ([]Hl7MessageQueueRow, error) {
	const sql = `
		SELECT id, listener_endpoint, peer_addr, received_at, mllp_message_id,
		       correlation_id, raw_body, key_version
		FROM hl7_message_queue
		WHERE processed = false
		ORDER BY received_at ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`

	rows, err := tx.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("hl7_message_queue: claim: %w", err)
	}
	defer rows.Close()

	out := make([]Hl7MessageQueueRow, 0, 8)
	for rows.Next() {
		var rec Hl7MessageQueueRow
		var enc []byte
		if err := rows.Scan(
			&rec.ID, &rec.ListenerEndpoint, &rec.PeerAddr, &rec.ReceivedAt,
			&rec.MllpMessageID, &rec.CorrelationID, &enc, &rec.KeyVersion,
		); err != nil {
			return nil, fmt.Errorf("hl7_message_queue: scan: %w", err)
		}
		body, err := r.codec.Decrypt(enc, rec.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("hl7_message_queue: decrypt: %w", err)
		}
		rec.RawBody = body
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hl7_message_queue: rows: %w", err)
	}
	return out, nil
}

// IncrementAttemptCount bumps attempt_count by 1 and returns the new
// value. Used by the hl7processor's BeginTx-failure path (S-9.9) to
// enforce a per-row retry budget without depending on the (failed)
// transaction. Runs on its own statement so a transient pool error
// on the main processOne tx does not block the increment.
func (r *Hl7MessageQueueRepo) IncrementAttemptCount(ctx context.Context, q Querier, id uuid.UUID) (int32, error) {
	const sql = `
		UPDATE hl7_message_queue
		SET attempt_count = attempt_count + 1
		WHERE id = $1
		RETURNING attempt_count`
	var n int32
	if err := q.QueryRow(ctx, sql, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("hl7_message_queue: increment attempt_count: %w", err)
	}
	return n, nil
}

// MarkProcessed flips processed=true and stamps processed_at on the row
// only if it was still false. Returns the number of rows affected
// (0 means already processed by another worker).
func (r *Hl7MessageQueueRepo) MarkProcessed(ctx context.Context, q Querier, id uuid.UUID) (int64, error) {
	const sql = `
		UPDATE hl7_message_queue
		SET processed = true, processed_at = now()
		WHERE id = $1 AND processed = false`
	tag, err := q.Exec(ctx, sql, id)
	if err != nil {
		return 0, fmt.Errorf("hl7_message_queue: mark: %w", err)
	}
	return tag.RowsAffected(), nil
}

// GetByID returns a single row by id, or nil if not found.
func (r *Hl7MessageQueueRepo) GetByID(ctx context.Context, q Querier, id uuid.UUID) (*Hl7MessageQueueRow, error) {
	const sql = `
		SELECT id, listener_endpoint, peer_addr, received_at, mllp_message_id,
		       correlation_id, raw_body, key_version, processed, processed_at,
		       attempt_count
		FROM hl7_message_queue
		WHERE id = $1`
	var rec Hl7MessageQueueRow
	var enc []byte
	row := q.QueryRow(ctx, sql, id)
	if err := row.Scan(
		&rec.ID, &rec.ListenerEndpoint, &rec.PeerAddr, &rec.ReceivedAt,
		&rec.MllpMessageID, &rec.CorrelationID, &enc, &rec.KeyVersion,
		&rec.Processed, &rec.ProcessedAt, &rec.AttemptCount,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("hl7_message_queue: get: %w", err)
	}
	body, err := r.codec.Decrypt(enc, rec.KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("hl7_message_queue: decrypt: %w", err)
	}
	rec.RawBody = body
	return &rec, nil
}
