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

// FindUnexpiredBySubscriptionAndClient returns the most recently
// issued unexpired (and unconsumed) row for (subscriptionID, clientID),
// or nil when no such row exists. It is the OP #241 idempotency
// lookup: $get-ws-binding-token consults this BEFORE mint so a
// repeat call from the same client within TTL returns the existing
// token instead of generating a fresh one.
//
// Notes:
//
//   - We hash-on-disk so we cannot return the cleartext token. The
//     caller is responsible for short-circuiting the response with
//     the token it already holds (the only recipient of the cleartext
//     was the original mint caller, who is the same caller again
//     under reuse). The handler does NOT short-circuit the cleartext
//     return — it falls through to the cache+DB path and rebuilds
//     the response from the cached cleartext token. The repo result
//     is therefore primarily for: (a) telling the handler "yes a
//     row exists", and (b) returning the row's expires_at so the
//     handler can compose the response with the original expiry.
//   - consumed_at is treated as terminal: a token that has been
//     bound is no longer reusable for fresh handshakes. This matches
//     the LLD §6 single-use redemption semantic.
//   - ORDER BY expires_at DESC, created_at DESC keeps the newest
//     row first so a caller that issued multiple unexpired tokens
//     gets the latest one (the cache typically only ever sees one).
func (r *WsBindingTokensRepo) FindUnexpiredBySubscriptionAndClient(
	ctx context.Context, q Querier, subscriptionID uuid.UUID, clientID string, now time.Time,
) (*WsBindingTokenRow, error) {
	const sql = `
		SELECT token, subscription_id, client_id, expires_at, created_at
		FROM ws_binding_tokens
		WHERE subscription_id = $1
		  AND client_id = $2
		  AND expires_at > $3
		  AND consumed_at IS NULL
		ORDER BY expires_at DESC, created_at DESC
		LIMIT 1`
	var row WsBindingTokenRow
	err := q.QueryRow(ctx, sql, subscriptionID, clientID, now).Scan(
		&row.Token,
		&row.SubscriptionID,
		&row.ClientID,
		&row.ExpiresAt,
		&row.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ws_binding_tokens: find unexpired: %w", err)
	}
	return &row, nil
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
