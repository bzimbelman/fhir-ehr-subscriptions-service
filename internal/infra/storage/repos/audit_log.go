// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"
)

// AuditLogRepo wraps the audit_log table.
type AuditLogRepo struct{}

// NewAuditLogRepo constructs the repo.
func NewAuditLogRepo() *AuditLogRepo { return &AuditLogRepo{} }

// Append writes one audit row and returns its assigned sequence number.
func (r *AuditLogRepo) Append(ctx context.Context, q Querier, row AuditLogRow) (int64, error) {
	const sql = `
		INSERT INTO audit_log
			(actor_kind, actor_id, action, target_kind, target_id, outcome,
			 correlation_id, canonical_form, hash, prev_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING seq`
	var seq int64
	if err := q.QueryRow(ctx, sql,
		row.ActorKind, row.ActorID, row.Action, row.TargetKind, row.TargetID,
		row.Outcome, row.CorrelationID, row.CanonicalForm, row.Hash, row.PrevHash,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("audit_log: append: %w", err)
	}
	return seq, nil
}
