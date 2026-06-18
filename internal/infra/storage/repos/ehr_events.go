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

// EhrEventsRepo wraps the ehr_events partitioned table.
type EhrEventsRepo struct {
	codec *codec.Codec
}

// NewEhrEventsRepo constructs the repo.
func NewEhrEventsRepo(c *codec.Codec) *EhrEventsRepo {
	return &EhrEventsRepo{codec: c}
}

// Insert persists one ehr_events row. Returns id and event_number.
func (r *EhrEventsRepo) Insert(ctx context.Context, q Querier, row EhrEventRow) (uuid.UUID, int64, error) {
	enc, kv, err := r.codec.Encrypt(row.Resource)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("ehr_events: encrypt resource: %w", err)
	}
	var prev []byte
	if len(row.PreviousResource) > 0 {
		prev, _, err = r.codec.Encrypt(row.PreviousResource)
		if err != nil {
			return uuid.Nil, 0, fmt.Errorf("ehr_events: encrypt previous: %w", err)
		}
	}
	// created_month is set explicitly so partition routing has a non-null
	// value; the v0 trigger normally sets it but Postgres routes
	// partitions before BEFORE triggers fire on the parent.
	const sql = `
		INSERT INTO ehr_events
			(topic_url, focus, change_kind, resource, previous_resource,
			 key_version, correlation_id, occurred_at, notification_shape_hint,
			 resource_change_id, created_month)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        date_trunc('month', now())::date)
		RETURNING id, event_number, created_month`
	var id uuid.UUID
	var ev int64
	var month any
	if err := q.QueryRow(ctx, sql,
		row.TopicURL, row.Focus, string(row.ChangeKind), enc, prev, kv,
		row.CorrelationID, row.OccurredAt, row.NotificationShapeHint, row.ResourceChangeID,
	).Scan(&id, &ev, &month); err != nil {
		return uuid.Nil, 0, fmt.Errorf("ehr_events: insert: %w", err)
	}
	return id, ev, nil
}

// ClaimUnprocessed pulls up to limit unprocessed rows under FOR UPDATE
// SKIP LOCKED.
func (r *EhrEventsRepo) ClaimUnprocessed(ctx context.Context, tx pgx.Tx, limit int32) ([]EhrEventRow, error) {
	const sql = `
		SELECT id, event_number, topic_url, focus, change_kind, resource,
		       previous_resource, key_version, correlation_id, occurred_at,
		       notification_shape_hint, resource_change_id, processed,
		       processed_at, created_month, created_at
		FROM ehr_events
		WHERE processed = false
		ORDER BY event_number ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("ehr_events: claim: %w", err)
	}
	defer rows.Close()
	out := make([]EhrEventRow, 0, 8)
	for rows.Next() {
		var rec EhrEventRow
		var enc, prev []byte
		var kind string
		if err := rows.Scan(
			&rec.ID, &rec.EventNumber, &rec.TopicURL, &rec.Focus, &kind, &enc,
			&prev, &rec.KeyVersion, &rec.CorrelationID, &rec.OccurredAt,
			&rec.NotificationShapeHint, &rec.ResourceChangeID, &rec.Processed,
			&rec.ProcessedAt, &rec.CreatedMonth, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("ehr_events: scan: %w", err)
		}
		rec.ChangeKind = ChangeKind(kind)
		body, err := r.codec.Decrypt(enc, rec.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("ehr_events: decrypt resource: %w", err)
		}
		rec.Resource = body
		if len(prev) > 0 {
			pb, err := r.codec.Decrypt(prev, rec.KeyVersion)
			if err != nil {
				return nil, fmt.Errorf("ehr_events: decrypt previous: %w", err)
			}
			rec.PreviousResource = pb
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ehr_events: rows: %w", err)
	}
	return out, nil
}

// GetByID returns one ehr_events row by id, decoding the payload
// columns through the codec. Returns (nil, nil) when not found.
//
// Used by the delivery scheduler to load the event a deliveries row
// references at dispatch time. The query touches every partition
// because the scheduler does not carry a created_month hint with the
// deliveries.ehr_event_id; for a single per-dispatch read this is
// acceptable. A (id, created_month) overload can be added if profile
// data demands partition pruning.
func (r *EhrEventsRepo) GetByID(ctx context.Context, q Querier, id uuid.UUID) (*EhrEventRow, error) {
	const sql = `
		SELECT id, event_number, topic_url, focus, change_kind, resource,
		       previous_resource, key_version, correlation_id, occurred_at,
		       notification_shape_hint, resource_change_id, processed,
		       processed_at, created_month, created_at
		FROM ehr_events
		WHERE id = $1`
	var rec EhrEventRow
	var enc, prev []byte
	var kind string
	row := q.QueryRow(ctx, sql, id)
	if err := row.Scan(
		&rec.ID, &rec.EventNumber, &rec.TopicURL, &rec.Focus, &kind, &enc,
		&prev, &rec.KeyVersion, &rec.CorrelationID, &rec.OccurredAt,
		&rec.NotificationShapeHint, &rec.ResourceChangeID, &rec.Processed,
		&rec.ProcessedAt, &rec.CreatedMonth, &rec.CreatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("ehr_events: get: %w", err)
	}
	rec.ChangeKind = ChangeKind(kind)
	body, err := r.codec.Decrypt(enc, rec.KeyVersion)
	if err != nil {
		return nil, fmt.Errorf("ehr_events: decrypt resource: %w", err)
	}
	rec.Resource = body
	if len(prev) > 0 {
		pb, err := r.codec.Decrypt(prev, rec.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("ehr_events: decrypt previous: %w", err)
		}
		rec.PreviousResource = pb
	}
	return &rec, nil
}

// MarkProcessed flips processed=true on the row guarded by created_month.
func (r *EhrEventsRepo) MarkProcessed(ctx context.Context, q Querier, id uuid.UUID, createdMonth any) (int64, error) {
	const sql = `
		UPDATE ehr_events
		SET processed = true, processed_at = now()
		WHERE id = $1 AND created_month = $2 AND processed = false`
	tag, err := q.Exec(ctx, sql, id, createdMonth)
	if err != nil {
		return 0, fmt.Errorf("ehr_events: mark: %w", err)
	}
	return tag.RowsAffected(), nil
}
