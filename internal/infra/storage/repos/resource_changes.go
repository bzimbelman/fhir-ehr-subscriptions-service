// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// ResourceChangesRepo wraps the resource_changes partitioned table.
type ResourceChangesRepo struct {
	codec *codec.Codec
}

// NewResourceChangesRepo constructs the repo.
func NewResourceChangesRepo(c *codec.Codec) *ResourceChangesRepo {
	return &ResourceChangesRepo{codec: c}
}

// Insert persists one resource_changes row. Resource and PreviousResource
// are encrypted under the codec's active key. Returns id and sequence.
func (r *ResourceChangesRepo) Insert(ctx context.Context, q Querier, row ResourceChangeRow) (uuid.UUID, int64, error) {
	enc, kv, err := r.codec.Encrypt(row.Resource)
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("resource_changes: encrypt resource: %w", err)
	}
	var prev []byte
	if len(row.PreviousResource) > 0 {
		prev, _, err = r.codec.Encrypt(row.PreviousResource)
		if err != nil {
			return uuid.Nil, 0, fmt.Errorf("resource_changes: encrypt previous: %w", err)
		}
	}

	// created_month is set explicitly here (in addition to the BEFORE
	// INSERT trigger in the v0 schema) so partition routing has a
	// non-null value to dispatch on. The trigger's behavior is identical;
	// we belt-and-suspender it because Postgres routes partitions before
	// BEFORE triggers fire on the parent partitioned relation.
	const sql = `
		INSERT INTO resource_changes
			(adapter_id, correlation_id, resource_type, change_kind,
			 resource, previous_resource, key_version, occurred_at, event_code,
			 created_month)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
		        date_trunc('month', now())::date)
		ON CONFLICT (adapter_id, correlation_id, created_month) DO NOTHING
		RETURNING id, sequence, created_month`

	var id uuid.UUID
	var seq int64
	var month any
	if err := q.QueryRow(ctx, sql,
		row.AdapterID, row.CorrelationID, row.ResourceType, string(row.ChangeKind),
		enc, prev, kv, row.OccurredAt, row.EventCode,
	).Scan(&id, &seq, &month); err != nil {
		return uuid.Nil, 0, fmt.Errorf("resource_changes: insert: %w", err)
	}
	return id, seq, nil
}

// ClaimUnprocessed pulls up to limit unprocessed rows under FOR UPDATE
// SKIP LOCKED.
func (r *ResourceChangesRepo) ClaimUnprocessed(ctx context.Context, tx pgx.Tx, limit int32) ([]ResourceChangeRow, error) {
	const sql = `
		SELECT id, sequence, adapter_id, correlation_id, resource_type,
		       change_kind, resource, previous_resource, key_version,
		       occurred_at, event_code, processed, created_month, created_at
		FROM resource_changes
		WHERE processed = false
		ORDER BY sequence ASC
		LIMIT $1
		FOR UPDATE SKIP LOCKED`
	rows, err := tx.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("resource_changes: claim: %w", err)
	}
	defer rows.Close()

	out := make([]ResourceChangeRow, 0, 8)
	for rows.Next() {
		var rec ResourceChangeRow
		var enc, prev []byte
		var kind string
		if err := rows.Scan(
			&rec.ID, &rec.Sequence, &rec.AdapterID, &rec.CorrelationID,
			&rec.ResourceType, &kind, &enc, &prev, &rec.KeyVersion,
			&rec.OccurredAt, &rec.EventCode, &rec.Processed, &rec.CreatedMonth, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("resource_changes: scan: %w", err)
		}
		rec.ChangeKind = ChangeKind(kind)
		body, err := r.codec.Decrypt(enc, rec.KeyVersion)
		if err != nil {
			return nil, fmt.Errorf("resource_changes: decrypt resource: %w", err)
		}
		rec.Resource = body
		if len(prev) > 0 {
			pb, err := r.codec.Decrypt(prev, rec.KeyVersion)
			if err != nil {
				return nil, fmt.Errorf("resource_changes: decrypt previous: %w", err)
			}
			rec.PreviousResource = pb
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resource_changes: rows: %w", err)
	}
	return out, nil
}

// MarkProcessed flips processed=true on the row guarded by created_month
// (the partitioning key).
func (r *ResourceChangesRepo) MarkProcessed(ctx context.Context, q Querier, id uuid.UUID, createdMonth any) (int64, error) {
	const sql = `
		UPDATE resource_changes
		SET processed = true
		WHERE id = $1 AND created_month = $2 AND processed = false`
	tag, err := q.Exec(ctx, sql, id, createdMonth)
	if err != nil {
		return 0, fmt.Errorf("resource_changes: mark: %w", err)
	}
	return tag.RowsAffected(), nil
}
