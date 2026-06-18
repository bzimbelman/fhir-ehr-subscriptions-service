// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AuthClientsRepo wraps the auth_clients table.
type AuthClientsRepo struct{}

// NewAuthClientsRepo constructs the repo.
func NewAuthClientsRepo() *AuthClientsRepo { return &AuthClientsRepo{} }

// Insert appends or upserts an auth client.
func (r *AuthClientsRepo) Insert(ctx context.Context, q Querier, row AuthClientRow) error {
	const sql = `
		INSERT INTO auth_clients (id, jwks_url, scopes, display_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE
			SET jwks_url = excluded.jwks_url,
			    scopes = excluded.scopes,
			    display_name = excluded.display_name,
			    updated_at = now()`
	_, err := q.Exec(ctx, sql, row.ID, row.JwksURL, row.Scopes, row.DisplayName)
	if err != nil {
		return fmt.Errorf("auth_clients: insert: %w", err)
	}
	return nil
}

// GetByID returns the row for client_id, or nil.
func (r *AuthClientsRepo) GetByID(ctx context.Context, q Querier, id string) (*AuthClientRow, error) {
	const sql = `
		SELECT id, COALESCE(jwks_url, ''), COALESCE(scopes, '{}'),
		       COALESCE(display_name, ''), created_at, updated_at
		FROM auth_clients
		WHERE id = $1`
	var rec AuthClientRow
	if err := q.QueryRow(ctx, sql, id).Scan(
		&rec.ID, &rec.JwksURL, &rec.Scopes, &rec.DisplayName,
		&rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("auth_clients: get: %w", err)
	}
	return &rec, nil
}
