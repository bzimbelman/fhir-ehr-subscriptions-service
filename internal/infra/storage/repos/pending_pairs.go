// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// PendingPairsRepo wraps the pending_pairs cancel-and-replace table.
type PendingPairsRepo struct {
	codec *codec.Codec
}

// NewPendingPairsRepo constructs the repo.
func NewPendingPairsRepo(c *codec.Codec) *PendingPairsRepo {
	return &PendingPairsRepo{codec: c}
}

// Insert writes a pending pair row. The pending_resource is encrypted.
func (r *PendingPairsRepo) Insert(ctx context.Context, q Querier, row PendingPairRow) error {
	enc, _, err := r.codec.Encrypt(row.PendingResource)
	if err != nil {
		return fmt.Errorf("pending_pairs: encrypt: %w", err)
	}
	const sql = `
		INSERT INTO pending_pairs
			(correlation_key, listener_endpoint, pending_resource, pending_kind,
			 source_message_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	_, err = q.Exec(ctx, sql,
		row.CorrelationKey, row.ListenerEndpoint, enc, string(row.PendingKind),
		row.SourceMessageID, row.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("pending_pairs: insert: %w", err)
	}
	return nil
}

// Delete removes a pending pair by its primary key.
func (r *PendingPairsRepo) Delete(ctx context.Context, q Querier, correlationKey, listenerEndpoint string) error {
	const sql = `
		DELETE FROM pending_pairs
		WHERE correlation_key = $1 AND listener_endpoint = $2`
	_, err := q.Exec(ctx, sql, correlationKey, listenerEndpoint)
	if err != nil {
		return fmt.Errorf("pending_pairs: delete: %w", err)
	}
	return nil
}

// ClaimExpired pulls up to limit rows whose expires_at is at or before
// now under FOR UPDATE SKIP LOCKED.
func (r *PendingPairsRepo) ClaimExpired(ctx context.Context, tx pgx.Tx, limit int32, now time.Time) ([]PendingPairRow, error) {
	const sql = `
		SELECT correlation_key, listener_endpoint, pending_resource, pending_kind,
		       source_message_id, expires_at, created_at
		FROM pending_pairs
		WHERE expires_at <= $1
		ORDER BY expires_at ASC
		LIMIT $2
		FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, sql, now, limit)
	if err != nil {
		return nil, fmt.Errorf("pending_pairs: claim: %w", err)
	}
	defer rows.Close()
	out := make([]PendingPairRow, 0, 4)
	for rows.Next() {
		var rec PendingPairRow
		var enc []byte
		var kind string
		if err := rows.Scan(
			&rec.CorrelationKey, &rec.ListenerEndpoint, &enc, &kind,
			&rec.SourceMessageID, &rec.ExpiresAt, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("pending_pairs: scan: %w", err)
		}
		body, err := r.codec.Decrypt(enc, 1)
		if err != nil {
			return nil, fmt.Errorf("pending_pairs: decrypt: %w", err)
		}
		rec.PendingResource = body
		rec.PendingKind = PendingKind(kind)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pending_pairs: rows: %w", err)
	}
	return out, nil
}
