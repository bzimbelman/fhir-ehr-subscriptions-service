// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// hashToken is the storage-boundary hash applied to WS bind tokens before
// they touch the database. Issued tokens are returned to subscribers
// cleartext over TLS, but the on-disk column stores sha256(cleartext) so a
// database leak never yields a usable bind credential. Both Insert and
// Consume must funnel through this helper or the lookup will never match.
func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ConsumeOutcome reports the result of a single-use token redemption.
type ConsumeOutcome int

// ConsumeOutcome values.
const (
	// ConsumeOK means the token existed, was unconsumed, and unexpired,
	// and is now marked consumed. The bound subscription_id is returned.
	ConsumeOK ConsumeOutcome = iota
	// ConsumeNotFound means no row exists for the supplied token.
	ConsumeNotFound
	// ConsumeAlreadyUsed means the token has been redeemed previously.
	ConsumeAlreadyUsed
	// ConsumeExpired means the token's expires_at is in the past.
	ConsumeExpired
)

// ConsumeResult is the typed result of a Consume call.
type ConsumeResult struct {
	Outcome        ConsumeOutcome
	SubscriptionID uuid.UUID
	ClientID       string
}

// WsBindingTokensRepo wraps the ws_binding_tokens table.
type WsBindingTokensRepo struct{}

// NewWsBindingTokensRepo constructs the repo.
func NewWsBindingTokensRepo() *WsBindingTokensRepo { return &WsBindingTokensRepo{} }

// Insert appends a token row. The caller passes the cleartext token; the
// repo hashes it before persisting so the on-disk column always holds
// sha256(cleartext).
func (r *WsBindingTokensRepo) Insert(ctx context.Context, q Querier, row WsBindingTokenRow) error {
	const sql = `
		INSERT INTO ws_binding_tokens (token, subscription_id, client_id, expires_at)
		VALUES ($1, $2, $3, $4)`
	_, err := q.Exec(ctx, sql, hashToken(row.Token), row.SubscriptionID, row.ClientID, row.ExpiresAt)
	if err != nil {
		return fmt.Errorf("ws_binding_tokens: insert: %w", err)
	}
	return nil
}

// Delete removes a token. Used for single-use redemption. Caller passes
// cleartext; the repo hashes to match the stored value.
func (r *WsBindingTokensRepo) Delete(ctx context.Context, q Querier, token string) error {
	const sql = `DELETE FROM ws_binding_tokens WHERE token = $1`
	_, err := q.Exec(ctx, sql, hashToken(token))
	if err != nil {
		return fmt.Errorf("ws_binding_tokens: delete: %w", err)
	}
	return nil
}

// Consume atomically marks a single-use token as consumed and returns the
// bound subscription. The redemption is fail-closed: if the token has been
// consumed before, has expired, or does not exist, the outcome reflects
// that and no row is mutated.
//
// The implementation uses a single UPDATE ... RETURNING so that two
// concurrent bind attempts cannot both succeed; the loser observes
// ConsumeAlreadyUsed.
func (r *WsBindingTokensRepo) Consume(ctx context.Context, q Querier, token string, now time.Time) (ConsumeResult, error) {
	const sql = `
		UPDATE ws_binding_tokens
		SET consumed_at = $2
		WHERE token = $1
		  AND consumed_at IS NULL
		  AND expires_at > $2
		RETURNING subscription_id, client_id`

	hashed := hashToken(token)
	var subID uuid.UUID
	var clientID string
	err := q.QueryRow(ctx, sql, hashed, now).Scan(&subID, &clientID)
	if err == nil {
		return ConsumeResult{
			Outcome:        ConsumeOK,
			SubscriptionID: subID,
			ClientID:       clientID,
		}, nil
	}
	if err != pgx.ErrNoRows {
		return ConsumeResult{}, fmt.Errorf("ws_binding_tokens: consume: %w", err)
	}

	// No row updated — diagnose why so the caller can surface a precise
	// reason (and so a replay is distinguishable from an expiry).
	const diag = `
		SELECT consumed_at IS NOT NULL, expires_at <= $2
		FROM ws_binding_tokens
		WHERE token = $1`
	var consumed, expired bool
	derr := q.QueryRow(ctx, diag, hashed, now).Scan(&consumed, &expired)
	if derr == pgx.ErrNoRows {
		return ConsumeResult{Outcome: ConsumeNotFound}, nil
	}
	if derr != nil {
		return ConsumeResult{}, fmt.Errorf("ws_binding_tokens: consume diag: %w", derr)
	}
	if consumed {
		return ConsumeResult{Outcome: ConsumeAlreadyUsed}, nil
	}
	if expired {
		return ConsumeResult{Outcome: ConsumeExpired}, nil
	}
	// Fell through despite zero-rows; treat as not found.
	return ConsumeResult{Outcome: ConsumeNotFound}, nil
}

// Get returns the row for a token, or nil if missing. Caller passes the
// cleartext token; the repo hashes to match the stored column value.
func (r *WsBindingTokensRepo) Get(ctx context.Context, q Querier, token string) (*WsBindingTokenRow, error) {
	const sql = `
		SELECT token, subscription_id, client_id, expires_at, created_at
		FROM ws_binding_tokens
		WHERE token = $1`
	var rec WsBindingTokenRow
	if err := q.QueryRow(ctx, sql, hashToken(token)).Scan(
		&rec.Token, &rec.SubscriptionID, &rec.ClientID, &rec.ExpiresAt, &rec.CreatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("ws_binding_tokens: get: %w", err)
	}
	return &rec, nil
}
