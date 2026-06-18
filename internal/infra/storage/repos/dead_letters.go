// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
)

// deadLetterReporter is the optional callback the wiring layer
// installs so it can bump fhir_subs_dead_letters_total{reason} once per
// successful Insert. The reporter is fire-and-forget; the repo never
// fails an Insert because the reporter panicked or returned slowly.
// Kept as a function pointer so this package stays free of a metrics
// dependency (P1.12).
var deadLetterReporter atomic.Pointer[func(reason string)]

// SetDeadLetterReporter installs (or unsets, with nil) the reporter
// invoked once per successful dead_letters insert with the row's Kind
// as the reason label.
func SetDeadLetterReporter(fn func(reason string)) {
	if fn == nil {
		deadLetterReporter.Store(nil)
		return
	}
	deadLetterReporter.Store(&fn)
}

func reportDeadLetter(reason string) {
	if r := deadLetterReporter.Load(); r != nil {
		(*r)(reason)
	}
}

// DeadLettersRepo wraps the dead_letters table.
type DeadLettersRepo struct {
	codec *codec.Codec
}

// NewDeadLettersRepo constructs the repo.
func NewDeadLettersRepo(c *codec.Codec) *DeadLettersRepo {
	return &DeadLettersRepo{codec: c}
}

// ListRecent returns the N most recent dead_letters rows for the admin
// surface (P1.6). Excludes the encrypted `payload_redacted` blob — the
// admin endpoint is for triage, not payload inspection; an operator
// who needs the payload must decrypt it offline with the codec.
func (r *DeadLettersRepo) ListRecent(ctx context.Context, q Querier, limit int) ([]DeadLetterRow, error) {
	if limit <= 0 {
		limit = 50
	}
	const sql = `
		SELECT id, kind, source_table, source_id, subscription_id,
		       reason, error_detail, correlation_id, created_at
		FROM dead_letters
		ORDER BY created_at DESC
		LIMIT $1`
	rows, err := q.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("dead_letters: list_recent: %w", err)
	}
	defer rows.Close()
	out := make([]DeadLetterRow, 0, limit)
	for rows.Next() {
		var rec DeadLetterRow
		if err := rows.Scan(
			&rec.ID, &rec.Kind, &rec.SourceTable, &rec.SourceID, &rec.SubscriptionID,
			&rec.Reason, &rec.ErrorDetail, &rec.CorrelationID, &rec.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("dead_letters: list_recent scan: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("dead_letters: list_recent rows: %w", err)
	}
	return out, nil
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
	reportDeadLetter(row.Kind)
	return id, nil
}
