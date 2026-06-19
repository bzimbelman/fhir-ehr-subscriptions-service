// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrAuditPrevHashMismatch is returned by AppendChained when the
// caller-supplied PrevHash does not match the prior row's hash in the
// audit_log table. This is a defense-in-depth check on top of the
// hash chain enforced by the observability/audit module: a buggy or
// malicious caller cannot pass an arbitrary prev_hash and bypass
// chain integrity.
var ErrAuditPrevHashMismatch = errors.New("audit_log: prev_hash does not match prior row")

// AuditLogRepo wraps the audit_log table.
type AuditLogRepo struct{}

// NewAuditLogRepo constructs the repo.
func NewAuditLogRepo() *AuditLogRepo { return &AuditLogRepo{} }

// Append writes one audit row and returns its assigned sequence
// number. The caller is fully responsible for the value of
// row.PrevHash — Append does not validate it. New callers should
// prefer AppendChained, which verifies the prev-hash against the
// prior row in the same transaction.
//
// Story #106: column names align with migration 0007 — chain_hash,
// prior_hash, chain_input — so the repos package and the
// observability/audit package read/write the same table without a
// schema mismatch.
func (r *AuditLogRepo) Append(ctx context.Context, q Querier, row AuditLogRow) (int64, error) {
	const sql = `
		INSERT INTO audit_log
			(actor_kind, actor_id, action, target_kind, target_id, outcome,
			 correlation_id, chain_input, chain_hash, prior_hash)
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

// AppendChained writes one audit row after verifying that the
// caller-supplied PrevHash matches the prior row's hash. The check
// and the insert run on the same Querier so they share whatever
// transaction the caller has open; the caller MUST run it inside a
// SERIALIZABLE transaction (or under the chain's advisory lock) to
// keep the verify→insert pair atomic.
//
// If the table is empty, the caller's PrevHash must equal the zero-
// length slice (or nil) to represent the genesis row. This is a
// belt-and-braces check on top of the chain logic in the
// observability/audit module.
func (r *AuditLogRepo) AppendChained(ctx context.Context, q Querier, row AuditLogRow) (int64, error) {
	const selSQL = `SELECT chain_hash FROM audit_log ORDER BY seq DESC LIMIT 1`
	var prior []byte
	switch err := q.QueryRow(ctx, selSQL).Scan(&prior); {
	case err == nil:
		if !bytes.Equal(prior, row.PrevHash) {
			return 0, ErrAuditPrevHashMismatch
		}
	case errors.Is(err, pgx.ErrNoRows):
		if len(row.PrevHash) != 0 {
			return 0, ErrAuditPrevHashMismatch
		}
	default:
		return 0, fmt.Errorf("audit_log: load prior: %w", err)
	}
	return r.Append(ctx, q, row)
}
