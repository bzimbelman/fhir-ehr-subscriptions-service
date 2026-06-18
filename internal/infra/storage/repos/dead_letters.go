// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// DeadLettersRepo wraps the dead_letters table.
type DeadLettersRepo struct {
	codec *codec.Codec
}

// NewDeadLettersRepo constructs the repo.
func NewDeadLettersRepo(c *codec.Codec) *DeadLettersRepo {
	return &DeadLettersRepo{codec: c}
}

// Insert appends a dead-letter row. payload_redacted is encrypted at rest.
func (r *DeadLettersRepo) Insert(ctx context.Context, q Querier, row DeadLetterRow) (uuid.UUID, error) {
	var enc []byte
	if len(row.PayloadRedacted) > 0 {
		var err error
		enc, _, err = r.codec.Encrypt(row.PayloadRedacted)
		if err != nil {
			return uuid.Nil, fmt.Errorf("dead_letters: encrypt: %w", err)
		}
	}

	const sql = `
		INSERT INTO dead_letters
			(kind, source_table, source_id, subscription_id, reason,
			 error_detail, payload_redacted, correlation_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`
	var id uuid.UUID
	if err := q.QueryRow(ctx, sql,
		row.Kind, row.SourceTable, row.SourceID, row.SubscriptionID,
		row.Reason, row.ErrorDetail, enc, row.CorrelationID,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("dead_letters: insert: %w", err)
	}
	return id, nil
}
