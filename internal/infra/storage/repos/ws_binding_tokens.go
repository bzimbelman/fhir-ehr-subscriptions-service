// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// WsBindingTokensRepo wraps the ws_binding_tokens table.
type WsBindingTokensRepo struct{}

// NewWsBindingTokensRepo constructs the repo.
func NewWsBindingTokensRepo() *WsBindingTokensRepo { return &WsBindingTokensRepo{} }

// Insert appends a token row.
func (r *WsBindingTokensRepo) Insert(ctx context.Context, q Querier, row WsBindingTokenRow) error {
	const sql = `
		INSERT INTO ws_binding_tokens (token, subscription_id, client_id, expires_at)
		VALUES ($1, $2, $3, $4)`
	_, err := q.Exec(ctx, sql, row.Token, row.SubscriptionID, row.ClientID, row.ExpiresAt)
	if err != nil {
		return fmt.Errorf("ws_binding_tokens: insert: %w", err)
	}
	return nil
}

// Delete removes a token. Used for single-use redemption.
func (r *WsBindingTokensRepo) Delete(ctx context.Context, q Querier, token string) error {
	const sql = `DELETE FROM ws_binding_tokens WHERE token = $1`
	_, err := q.Exec(ctx, sql, token)
	if err != nil {
		return fmt.Errorf("ws_binding_tokens: delete: %w", err)
	}
	return nil
}

// Get returns the row for a token, or nil if missing.
func (r *WsBindingTokensRepo) Get(ctx context.Context, q Querier, token string) (*WsBindingTokenRow, error) {
	const sql = `
		SELECT token, subscription_id, client_id, expires_at, created_at
		FROM ws_binding_tokens
		WHERE token = $1`
	var rec WsBindingTokenRow
	if err := q.QueryRow(ctx, sql, token).Scan(
		&rec.Token, &rec.SubscriptionID, &rec.ClientID, &rec.ExpiresAt, &rec.CreatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("ws_binding_tokens: get: %w", err)
	}
	return &rec, nil
}
