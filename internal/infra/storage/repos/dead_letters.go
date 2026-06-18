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
