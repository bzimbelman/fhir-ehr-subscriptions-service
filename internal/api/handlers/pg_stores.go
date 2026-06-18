// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// maxPageSize and maxEventReplayPageSize are defensive ceilings for
// the LIMIT-bound int32 in pgx; production traffic is already capped
// upstream by parseCountParam / Deps.EventReplayPageSize but a buggy
// internal caller (or future code path) cannot overflow the SQL bind.
const (
	maxPageSize            = 10_000
	maxEventReplayPageSize = 100_000
)

// PgSubscriptionsStore wraps repos.SubscriptionsRepo with a pool plus
// the extra methods (UpdateResource, UpdateStatus, ListByClient) the
// API needs that are not part of the existing repo's surface.
type PgSubscriptionsStore struct {
	Pool *pgxpool.Pool
	Repo *repos.SubscriptionsRepo
}

// NewPgSubscriptionsStore is a convenience constructor.
func NewPgSubscriptionsStore(pool *pgxpool.Pool) *PgSubscriptionsStore {
	return &PgSubscriptionsStore{Pool: pool, Repo: repos.NewSubscriptionsRepo()}
}

// Insert delegates to the existing repo on a pool-checked-out connection.
func (s *PgSubscriptionsStore) Insert(ctx context.Context, row repos.SubscriptionRow) (uuid.UUID, error) {
	return s.Repo.Insert(ctx, s.Pool, row)
}

// GetByID delegates to the existing repo.
func (s *PgSubscriptionsStore) GetByID(ctx context.Context, id uuid.UUID) (*repos.SubscriptionRow, error) {
	return s.Repo.GetByID(ctx, s.Pool, id)
}

// ListByClient queries subscriptions by client id. The existing repo
// has no equivalent; the small scope here justifies an inline query.
func (s *PgSubscriptionsStore) ListByClient(ctx context.Context, clientID string) ([]repos.SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE client_id = $1
		ORDER BY created_at DESC`
	rows, err := s.Pool.Query(ctx, sql, clientID)
	if err != nil {
		return nil, fmt.Errorf("subscriptions: list by client: %w", err)
	}
	defer rows.Close()
	out := make([]repos.SubscriptionRow, 0)
	for rows.Next() {
		var rec repos.SubscriptionRow
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
		rec.Status = repos.SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListByClientPage returns up to limit subscription rows for clientID
// ordered by created_at DESC, id DESC, optionally starting strictly
// after the (CreatedAt, ID) cursor. The unique strict ordering on the
// (created_at, id) pair guarantees consecutive pages do not overlap and
// concurrent inserts cannot cause an existing row to be skipped on the
// cursor's sort axis. (S-2.8)
func (s *PgSubscriptionsStore) ListByClientPage(ctx context.Context, clientID string, after *SubscriptionCursor, limit int) ([]repos.SubscriptionRow, error) {
	if limit <= 0 {
		return nil, nil
	}
	// Defensive cap so an unsanitized caller cannot overflow int32 in
	// the LIMIT bind; production traffic comes from parseCountParam,
	// which already clamps to SearchMaxPageSize.
	if limit > maxPageSize {
		limit = maxPageSize
	}
	const baseSQL = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE client_id = $1`
	var (
		rows pgx.Rows
		err  error
	)
	//nolint:gosec // limit is bounded above by maxPageSize which fits int32
	limit32 := int32(limit)
	if after == nil {
		rows, err = s.Pool.Query(ctx, baseSQL+`
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, clientID, limit32)
	} else {
		rows, err = s.Pool.Query(ctx, baseSQL+`
		  AND (created_at, id) < ($2, $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4`, clientID, after.CreatedAt, after.ID, limit32)
	}
	if err != nil {
		return nil, fmt.Errorf("subscriptions: list by client page: %w", err)
	}
	defer rows.Close()
	out := make([]repos.SubscriptionRow, 0, limit)
	for rows.Next() {
		var rec repos.SubscriptionRow
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
		rec.Status = repos.SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// FindByClientAndCriteria runs the If-None-Exist (LLD §4.1) match in
// SQL: every supplied predicate (topic / channel type / endpoint) is
// folded into the WHERE clause along with `client_id = $1` and
// `status <> 'off'`. The database returns at most one row even when
// many subscriptions exist for the client (S-2.4 — predicate is
// pushed into the index instead of materializing the entire client
// list in Go).
func (s *PgSubscriptionsStore) FindByClientAndCriteria(ctx context.Context, clientID string, criteria SubscriptionMatchCriteria) ([]repos.SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE client_id = $1
		  AND status <> 'off'
		  AND ($2 = '' OR topic_url = $2)
		  AND ($3 = '' OR channel_type = $3)
		  AND ($4 = '' OR COALESCE(endpoint, '') = $4)
		LIMIT 1`
	rows, err := s.Pool.Query(ctx, sql, clientID, criteria.Topic, criteria.ChannelType, criteria.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("subscriptions: find by criteria: %w", err)
	}
	defer rows.Close()
	out := make([]repos.SubscriptionRow, 0, 1)
	for rows.Next() {
		var rec repos.SubscriptionRow
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
		rec.Status = repos.SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateResource overwrites the row's mutable fields. Audit/version
// state is up to the caller.
func (s *PgSubscriptionsStore) UpdateResource(ctx context.Context, id uuid.UUID, row repos.SubscriptionRow) error {
	const sql = `
		UPDATE subscriptions SET
			topic_url = $2,
			channel_type = $3,
			endpoint = $4,
			header = $5,
			filter_by = $6,
			content = $7,
			heartbeat_period = $8,
			timeout = $9,
			max_count = $10,
			reason = $11,
			end_time = $12,
			contact = $13,
			updated_at = now()
		WHERE id = $1`
	_, err := s.Pool.Exec(ctx, sql,
		id,
		row.TopicURL, row.ChannelType, row.Endpoint, row.Header, row.FilterBy,
		row.Content, row.HeartbeatPeriod, row.Timeout, row.MaxCount,
		row.Reason, row.EndTime, row.Contact,
	)
	if err != nil {
		return fmt.Errorf("subscriptions: update: %w", err)
	}
	return nil
}

// UpdateStatus transitions the row's status. errMsg is recorded on the
// `error` column.
func (s *PgSubscriptionsStore) UpdateStatus(ctx context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error {
	const sql = `
		UPDATE subscriptions
		SET status = $2,
		    error = NULLIF($3, ''),
		    last_handshake_at = CASE WHEN $2 = 'active' THEN now() ELSE last_handshake_at END,
		    updated_at = now()
		WHERE id = $1`
	_, err := s.Pool.Exec(ctx, sql, id, string(status), errMsg)
	if err != nil {
		return fmt.Errorf("subscriptions: update status: %w", err)
	}
	return nil
}

// PgTopicsStore wraps repos.SubscriptionTopicsRepo for ListActive.
type PgTopicsStore struct {
	Pool *pgxpool.Pool
	Repo *repos.SubscriptionTopicsRepo
}

// NewPgTopicsStore is a convenience constructor.
func NewPgTopicsStore(pool *pgxpool.Pool) *PgTopicsStore {
	return &PgTopicsStore{Pool: pool, Repo: repos.NewSubscriptionTopicsRepo()}
}

// ListActive returns all topic rows whose status='active'.
func (s *PgTopicsStore) ListActive(ctx context.Context) ([]repos.SubscriptionTopicRow, error) {
	return s.Repo.ListByStatus(ctx, s.Pool, "active")
}

// PgEventsStore implements EhrEventsStore for $events replay. We add a
// single read query directly to avoid touching the repo.
type PgEventsStore struct {
	Pool *pgxpool.Pool
}

// NewPgEventsStore constructs the store.
func NewPgEventsStore(pool *pgxpool.Pool) *PgEventsStore {
	return &PgEventsStore{Pool: pool}
}

// ListByTopicAndRange is a read of ehr_events filtered by topic and
// event_number range. since/until of 0 mean unbounded.
func (s *PgEventsStore) ListByTopicAndRange(ctx context.Context, topicURL string, since, until int64) ([]repos.EhrEventRow, error) {
	const sql = `
		SELECT id, event_number, topic_url, focus, change_kind, occurred_at,
		       resource_change_id
		FROM ehr_events
		WHERE topic_url = $1
		  AND ($2 = 0 OR event_number >= $2)
		  AND ($3 = 0 OR event_number <= $3)
		ORDER BY event_number ASC
		LIMIT 1000`
	rows, err := s.Pool.Query(ctx, sql, topicURL, since, until)
	if err != nil {
		return nil, fmt.Errorf("ehr_events: replay: %w", err)
	}
	defer rows.Close()
	out := make([]repos.EhrEventRow, 0)
	for rows.Next() {
		var rec repos.EhrEventRow
		var kind string
		if err := rows.Scan(
			&rec.ID, &rec.EventNumber, &rec.TopicURL, &rec.Focus, &kind,
			&rec.OccurredAt, &rec.ResourceChangeID,
		); err != nil {
			return nil, fmt.Errorf("ehr_events: scan: %w", err)
		}
		rec.ChangeKind = repos.ChangeKind(kind)
		out = append(out, rec)
	}
	return out, nil
}

// ListByTopicAndRangePage replaces the legacy hardcoded LIMIT 1000 in
// ListByTopicAndRange with an explicit page size. limit <= 0 means
// "no cap" (the implementation falls back to a generous SQL-side cap
// to keep an unbounded query from pinning the pool); production
// callers always pass a positive value derived from
// Deps.EventReplayPageSize. (S-2.15)
func (s *PgEventsStore) ListByTopicAndRangePage(ctx context.Context, topicURL string, since, until int64, limit int) ([]repos.EhrEventRow, error) {
	if limit <= 0 {
		// Defensive cap so a buggy caller cannot exhaust the pool. The
		// production path never hits this because the handler always
		// passes a positive limit from Deps.EventReplayPageSize.
		limit = maxEventReplayPageSize
	}
	if limit > maxEventReplayPageSize {
		limit = maxEventReplayPageSize
	}
	const sql = `
		SELECT id, event_number, topic_url, focus, change_kind, occurred_at,
		       resource_change_id
		FROM ehr_events
		WHERE topic_url = $1
		  AND ($2 = 0 OR event_number >= $2)
		  AND ($3 = 0 OR event_number <= $3)
		ORDER BY event_number ASC
		LIMIT $4`
	//nolint:gosec // limit is bounded above by maxEventReplayPageSize which fits int32
	limit32 := int32(limit)
	rows, err := s.Pool.Query(ctx, sql, topicURL, since, until, limit32)
	if err != nil {
		return nil, fmt.Errorf("ehr_events: replay page: %w", err)
	}
	defer rows.Close()
	out := make([]repos.EhrEventRow, 0)
	for rows.Next() {
		var rec repos.EhrEventRow
		var kind string
		if err := rows.Scan(
			&rec.ID, &rec.EventNumber, &rec.TopicURL, &rec.Focus, &kind,
			&rec.OccurredAt, &rec.ResourceChangeID,
		); err != nil {
			return nil, fmt.Errorf("ehr_events: scan: %w", err)
		}
		rec.ChangeKind = repos.ChangeKind(kind)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// PgDeliveriesStore implements DeliveriesStore for $status. The
// existing repo doesn't expose lastDeliveredEventNumber so we run the
// query inline here.
type PgDeliveriesStore struct {
	Pool *pgxpool.Pool
}

// NewPgDeliveriesStore constructs the store.
func NewPgDeliveriesStore(pool *pgxpool.Pool) *PgDeliveriesStore {
	return &PgDeliveriesStore{Pool: pool}
}

// LastDeliveredEventNumber reads the highest event_number whose status
// is 'delivered' for the given subscription.
func (s *PgDeliveriesStore) LastDeliveredEventNumber(ctx context.Context, sub uuid.UUID) (int64, error) {
	const sql = `
		SELECT COALESCE(MAX(event_number), 0)
		FROM deliveries
		WHERE subscription_id = $1 AND status = 'delivered'`
	var n int64
	err := s.Pool.QueryRow(ctx, sql, sub).Scan(&n)
	if err != nil && err != pgx.ErrNoRows {
		return 0, fmt.Errorf("deliveries: last: %w", err)
	}
	return n, nil
}

// PgWsBindingTokensStore wraps repos.WsBindingTokensRepo.
type PgWsBindingTokensStore struct {
	Pool *pgxpool.Pool
	Repo *repos.WsBindingTokensRepo
}

// NewPgWsBindingTokensStore is a convenience constructor.
func NewPgWsBindingTokensStore(pool *pgxpool.Pool) *PgWsBindingTokensStore {
	return &PgWsBindingTokensStore{Pool: pool, Repo: repos.NewWsBindingTokensRepo()}
}

// Insert delegates to the existing repo.
func (s *PgWsBindingTokensStore) Insert(ctx context.Context, row repos.WsBindingTokenRow) error {
	return s.Repo.Insert(ctx, s.Pool, row)
}

// PgAuditStore is a minimal AuditStore that writes a degenerate row to
// audit_log. The real audit module owns hash chaining; the API just
// records an event so integration tests can assert the trail exists.
type PgAuditStore struct {
	Pool *pgxpool.Pool
	Repo *repos.AuditLogRepo
}

// NewPgAuditStore constructs the store.
func NewPgAuditStore(pool *pgxpool.Pool) *PgAuditStore {
	return &PgAuditStore{Pool: pool, Repo: repos.NewAuditLogRepo()}
}

// Append writes a degenerate audit row (no hash-chain integrity) so the
// integration tests can observe the API recording events. Production
// deployments should wire the observability/audit module's hash-chained
// store instead — see infra/observability/audit.
func (s *PgAuditStore) Append(ctx context.Context, action, target, outcome string, correlationID *uuid.UUID, canonical []byte) error {
	if len(canonical) == 0 {
		canonical = []byte("{}")
	}
	row := repos.AuditLogRow{
		ActorKind:     "subscriber",
		Action:        action,
		TargetKind:    "Subscription",
		TargetID:      target,
		Outcome:       outcome,
		CorrelationID: correlationID,
		CanonicalForm: canonical,
		Hash:          []byte{0},
	}
	_, err := s.Repo.Append(ctx, s.Pool, row)
	if err != nil {
		return fmt.Errorf("audit: append: %w", err)
	}
	return nil
}

// AuthClientLookup wraps repos.AuthClientsRepo so the Verifier can
// resolve client records from the auth_clients table.
type AuthClientLookup struct {
	Pool *pgxpool.Pool
	Repo *repos.AuthClientsRepo
}

// NewAuthClientLookup constructs the lookup adapter.
func NewAuthClientLookup(pool *pgxpool.Pool) *AuthClientLookup {
	return &AuthClientLookup{Pool: pool, Repo: repos.NewAuthClientsRepo()}
}

// GetByID delegates to the existing repo.
func (a *AuthClientLookup) GetByID(ctx context.Context, id string) (*auth.ClientRecord, error) {
	row, err := a.Repo.GetByID(ctx, a.Pool, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &auth.ClientRecord{
		ID:      row.ID,
		JwksURL: row.JwksURL,
		Scopes:  row.Scopes,
	}, nil
}
