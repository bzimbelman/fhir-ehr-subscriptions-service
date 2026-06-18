// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AdapterStateRepo wraps the adapter_state KV table.
type AdapterStateRepo struct{}

// NewAdapterStateRepo constructs the repo.
func NewAdapterStateRepo() *AdapterStateRepo { return &AdapterStateRepo{} }

// Upsert sets a value at (adapter_id, scope, key).
func (r *AdapterStateRepo) Upsert(ctx context.Context, q Querier, row AdapterStateRow) error {
	const sql = `
		INSERT INTO adapter_state (adapter_id, scope, key, value, key_version, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (adapter_id, scope, key)
		DO UPDATE SET value = excluded.value,
		              key_version = excluded.key_version,
		              updated_at = now()`
	kv := row.KeyVersion
	if kv == 0 {
		kv = 1
	}
	_, err := q.Exec(ctx, sql, row.AdapterID, row.Scope, row.Key, row.Value, kv)
	if err != nil {
		return fmt.Errorf("adapter_state: upsert: %w", err)
	}
	return nil
}

// Get returns a single row, or nil if not found.
func (r *AdapterStateRepo) Get(ctx context.Context, q Querier, adapterID, scope, key string) (*AdapterStateRow, error) {
	const sql = `
		SELECT value, key_version, updated_at
		FROM adapter_state
		WHERE adapter_id = $1 AND scope = $2 AND key = $3`
	var rec AdapterStateRow
	rec.AdapterID, rec.Scope, rec.Key = adapterID, scope, key
	if err := q.QueryRow(ctx, sql, adapterID, scope, key).Scan(
		&rec.Value, &rec.KeyVersion, &rec.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("adapter_state: get: %w", err)
	}
	return &rec, nil
}
