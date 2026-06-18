// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SubscriptionTopicsRepo wraps the subscription_topics table.
type SubscriptionTopicsRepo struct{}

// NewSubscriptionTopicsRepo constructs the repo.
func NewSubscriptionTopicsRepo() *SubscriptionTopicsRepo { return &SubscriptionTopicsRepo{} }

// Insert appends a row. Returns the assigned UUID.
func (r *SubscriptionTopicsRepo) Insert(ctx context.Context, q Querier, row SubscriptionTopicRow) (uuid.UUID, error) {
	const sql = `
		INSERT INTO subscription_topics
			(url, version, title, description, status, date, source, body, compiled_form)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`
	var id uuid.UUID
	if err := q.QueryRow(ctx, sql,
		row.URL, row.Version, row.Title, row.Description, row.Status,
		row.Date, row.Source, row.Body, row.CompiledForm,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("subscription_topics: insert: %w", err)
	}
	return id, nil
}

// GetByURLVersion returns the row matching (url, version), or nil.
func (r *SubscriptionTopicsRepo) GetByURLVersion(ctx context.Context, q Querier, url, version string) (*SubscriptionTopicRow, error) {
	const sql = `
		SELECT id, url, version, COALESCE(title, ''), COALESCE(description, ''),
		       status, date, source, body, compiled_form, created_at, retired_at
		FROM subscription_topics
		WHERE url = $1 AND version = $2`
	var rec SubscriptionTopicRow
	if err := q.QueryRow(ctx, sql, url, version).Scan(
		&rec.ID, &rec.URL, &rec.Version, &rec.Title, &rec.Description,
		&rec.Status, &rec.Date, &rec.Source, &rec.Body, &rec.CompiledForm,
		&rec.CreatedAt, &rec.RetiredAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("subscription_topics: get: %w", err)
	}
	return &rec, nil
}

// ListByStatus returns all rows in the given status.
func (r *SubscriptionTopicsRepo) ListByStatus(ctx context.Context, q Querier, status string) ([]SubscriptionTopicRow, error) {
	const sql = `
		SELECT id, url, version, COALESCE(title, ''), COALESCE(description, ''),
		       status, date, source, body, compiled_form, created_at, retired_at
		FROM subscription_topics
		WHERE status = $1
		ORDER BY url, version`
	rows, err := q.Query(ctx, sql, status)
	if err != nil {
		return nil, fmt.Errorf("subscription_topics: list: %w", err)
	}
	defer rows.Close()
	out := make([]SubscriptionTopicRow, 0, 8)
	for rows.Next() {
		var rec SubscriptionTopicRow
		if err := rows.Scan(
			&rec.ID, &rec.URL, &rec.Version, &rec.Title, &rec.Description,
			&rec.Status, &rec.Date, &rec.Source, &rec.Body, &rec.CompiledForm,
			&rec.CreatedAt, &rec.RetiredAt,
		); err != nil {
			return nil, fmt.Errorf("subscription_topics: scan: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("subscription_topics: rows: %w", err)
	}
	return out, nil
}
