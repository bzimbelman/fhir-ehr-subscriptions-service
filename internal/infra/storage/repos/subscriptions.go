// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SubscriptionsRepo wraps the subscriptions table.
type SubscriptionsRepo struct{}

// NewSubscriptionsRepo constructs the repo.
func NewSubscriptionsRepo() *SubscriptionsRepo { return &SubscriptionsRepo{} }

// Insert creates a new subscription row.
func (r *SubscriptionsRepo) Insert(ctx context.Context, q Querier, row SubscriptionRow) (uuid.UUID, error) {
	const sql = `
		INSERT INTO subscriptions
			(client_id, status, topic_url, channel_type, endpoint, header,
			 filter_by, content, heartbeat_period, timeout, max_count,
			 events_since_subscription_start, reason, end_time, error,
			 contact, last_handshake_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id`
	maxCount := row.MaxCount
	if maxCount == 0 {
		maxCount = 1
	}
	content := row.Content
	if content == "" {
		content = "id-only"
	}
	var id uuid.UUID
	if err := q.QueryRow(ctx, sql,
		row.ClientID, string(row.Status), row.TopicURL, row.ChannelType,
		row.Endpoint, row.Header, row.FilterBy, content,
		row.HeartbeatPeriod, row.Timeout, maxCount,
		row.EventsSinceSubscriptionStart, row.Reason, row.EndTime,
		row.Error, row.Contact, row.LastHandshakeAt,
	).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("subscriptions: insert: %w", err)
	}
	return id, nil
}

// GetByID returns one subscription by id, or nil.
func (r *SubscriptionsRepo) GetByID(ctx context.Context, q Querier, id uuid.UUID) (*SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE id = $1`
	var rec SubscriptionRow
	var status string
	if err := q.QueryRow(ctx, sql, id).Scan(
		&rec.ID, &rec.ClientID, &status, &rec.TopicURL, &rec.ChannelType,
		&rec.Endpoint, &rec.Header, &rec.FilterBy, &rec.Content,
		&rec.HeartbeatPeriod, &rec.Timeout, &rec.MaxCount,
		&rec.EventsSinceSubscriptionStart, &rec.Reason, &rec.EndTime,
		&rec.Error, &rec.Contact, &rec.LastHandshakeAt,
		&rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("subscriptions: get: %w", err)
	}
	rec.Status = SubscriptionStatus(status)
	return &rec, nil
}

// ListActiveByTopic returns all active subscriptions for a topic.
func (r *SubscriptionsRepo) ListActiveByTopic(ctx context.Context, q Querier, topicURL string) ([]SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE topic_url = $1 AND status = 'active'`
	rows, err := q.Query(ctx, sql, topicURL)
	if err != nil {
		return nil, fmt.Errorf("subscriptions: list: %w", err)
	}
	defer rows.Close()
	out := make([]SubscriptionRow, 0, 4)
	for rows.Next() {
		var rec SubscriptionRow
		var status string
		if err := rows.Scan(
			&rec.ID, &rec.ClientID, &status, &rec.TopicURL, &rec.ChannelType,
			&rec.Endpoint, &rec.Header, &rec.FilterBy, &rec.Content,
			&rec.HeartbeatPeriod, &rec.Timeout, &rec.MaxCount,
			&rec.EventsSinceSubscriptionStart, &rec.Reason, &rec.EndTime,
			&rec.Error, &rec.Contact, &rec.LastHandshakeAt,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("subscriptions: scan: %w", err)
		}
		rec.Status = SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("subscriptions: rows: %w", err)
	}
	return out, nil
}
