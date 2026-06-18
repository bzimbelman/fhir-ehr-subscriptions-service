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
//
// Memory: this returns every active subscription for the topic in a
// single slice. For very large catalogs (>10k subscribers per topic)
// callers should prefer StreamActiveByTopic or ListActiveByTopicPage,
// which bound peak memory.
func (r *SubscriptionsRepo) ListActiveByTopic(ctx context.Context, q Querier, topicURL string) ([]SubscriptionRow, error) {
	out := make([]SubscriptionRow, 0, 4)
	if err := r.StreamActiveByTopic(ctx, q, topicURL, func(row SubscriptionRow) error {
		out = append(out, row)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// StreamActiveByTopic invokes fn once per active subscription for the
// given topic, in id order. Returning a non-nil error from fn aborts
// iteration and is propagated to the caller. The fanout worker uses
// this to keep peak memory flat as the active-subscription set grows.
func (r *SubscriptionsRepo) StreamActiveByTopic(
	ctx context.Context, q Querier, topicURL string,
	fn func(SubscriptionRow) error,
) error {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE topic_url = $1 AND status = 'active'
		ORDER BY id`
	rows, err := q.Query(ctx, sql, topicURL)
	if err != nil {
		return fmt.Errorf("subscriptions: list: %w", err)
	}
	defer rows.Close()
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
			return fmt.Errorf("subscriptions: scan: %w", err)
		}
		rec.Status = SubscriptionStatus(status)
		if err := fn(rec); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("subscriptions: rows: %w", err)
	}
	return nil
}

// ListActiveByTopicPage returns up to limit active subscriptions for a
// topic with id strictly greater than afterID. Pass uuid.Nil for the
// first page and the last row's id for subsequent pages. The id-order
// keyset means a paginated scan never revisits a row even under
// concurrent inserts.
func (r *SubscriptionsRepo) ListActiveByTopicPage(
	ctx context.Context, q Querier, topicURL string, afterID uuid.UUID, limit int32,
) ([]SubscriptionRow, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("subscriptions: list-page: limit must be > 0")
	}
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE topic_url = $1 AND status = 'active' AND id > $2
		ORDER BY id
		LIMIT $3`
	rows, err := q.Query(ctx, sql, topicURL, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("subscriptions: list-page: %w", err)
	}
	defer rows.Close()
	out := make([]SubscriptionRow, 0, limit)
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
