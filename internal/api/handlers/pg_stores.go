// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
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

// QueryTimeouts bounds how long a single Pg query may run before the
// per-query context deadline fires (S-2.14). Two knobs because read and
// write paths have different latency expectations; both are exposed
// under the YAML `storage.query_timeouts` block so operators can tune
// them without code changes. Defaults follow the storage.md guidance:
// 5s read / 10s write — long enough that a healthy DB never trips them,
// tight enough that a stuck query can't pin a request handler.
type QueryTimeouts struct {
	// Read bounds SELECT-style queries. Default: 5s.
	Read time.Duration
	// Write bounds INSERT/UPDATE/DELETE/Exec calls. Default: 10s.
	Write time.Duration
}

// ApplyDefaults fills zero-valued knobs with the storage.md defaults.
// Operator-supplied values are preserved.
func (q *QueryTimeouts) ApplyDefaults() {
	if q.Read <= 0 {
		q.Read = 5 * time.Second
	}
	if q.Write <= 0 {
		q.Write = 10 * time.Second
	}
}

// ErrQueryTimeout is the typed sentinel returned by every pg_stores
// query when a per-query deadline fires. Callers translate this to a
// 504 Gateway Timeout / OperationOutcome timeout code; the typed error
// distinguishes a "the database is taking too long" surface from a
// caller-cancelled request (which keeps the parent context error).
var ErrQueryTimeout = errors.New("storage: query deadline exceeded")

// queryTimeoutError wraps ErrQueryTimeout with a scope tag so logs can
// attribute which query site fired the deadline without stringly
// matching error text.
type queryTimeoutError struct {
	scope string
}

func (e *queryTimeoutError) Error() string {
	return fmt.Sprintf("storage: query deadline exceeded (%s)", e.scope)
}

func (e *queryTimeoutError) Is(target error) bool {
	return target == ErrQueryTimeout
}

// WrapQueryTimeout returns a typed timeout error. errors.Is(err,
// ErrQueryTimeout) returns true on the result. Exposed for tests and
// for callers that synthesize a deadline-exceeded surface without
// going through TranslateQueryErr.
func WrapQueryTimeout(_ error, scope string) error {
	return &queryTimeoutError{scope: scope}
}

// TranslateQueryErr rewrites a pgx error into ErrQueryTimeout when the
// inner per-query context fired but the parent context did NOT. The
// distinction matters: a caller-cancelled request must keep the
// parent's context error so handlers attribute the cancellation to the
// caller, not the database. Non-deadline errors pass through with
// %w-wrapping so errors.Is still matches the original.
func TranslateQueryErr(parent, inner context.Context, err error, scope string) error {
	if err == nil {
		return nil
	}
	// Parent already done — return parent's cause unchanged so the
	// caller sees their own cancellation/deadline rather than the
	// query's.
	if pErr := parent.Err(); pErr != nil {
		return fmt.Errorf("%s: %w", scope, err)
	}
	if errors.Is(inner.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return WrapQueryTimeout(err, scope)
	}
	return fmt.Errorf("%s: %w", scope, err)
}

// withRead derives a per-query context bounded by qt.Read.
func (qt QueryTimeouts) withRead(parent context.Context) (context.Context, context.CancelFunc) {
	t := qt.Read
	if t <= 0 {
		t = 5 * time.Second
	}
	return context.WithTimeout(parent, t)
}

// withWrite derives a per-query context bounded by qt.Write.
func (qt QueryTimeouts) withWrite(parent context.Context) (context.Context, context.CancelFunc) {
	t := qt.Write
	if t <= 0 {
		t = 10 * time.Second
	}
	return context.WithTimeout(parent, t)
}

// PgSubscriptionsStore wraps repos.SubscriptionsRepo with a pool plus
// the extra methods (UpdateResource, UpdateStatus, ListByClient) the
// API needs that are not part of the existing repo's surface.
type PgSubscriptionsStore struct {
	Pool     *pgxpool.Pool
	Repo     *repos.SubscriptionsRepo
	Timeouts QueryTimeouts
}

// NewPgSubscriptionsStore is a convenience constructor that uses the
// default per-query deadlines (5s read / 10s write).
func NewPgSubscriptionsStore(pool *pgxpool.Pool) *PgSubscriptionsStore {
	return NewPgSubscriptionsStoreWithTimeouts(pool, QueryTimeouts{})
}

// NewPgSubscriptionsStoreWithTimeouts lets the operator override the
// per-query deadlines. Zero-valued fields fall back to the defaults.
func NewPgSubscriptionsStoreWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *PgSubscriptionsStore {
	qt.ApplyDefaults()
	return &PgSubscriptionsStore{Pool: pool, Repo: repos.NewSubscriptionsRepo(), Timeouts: qt}
}

// Insert delegates to the existing repo on a pool-checked-out connection.
func (s *PgSubscriptionsStore) Insert(ctx context.Context, row repos.SubscriptionRow) (uuid.UUID, error) {
	qctx, cancel := s.Timeouts.withWrite(ctx)
	defer cancel()
	id, err := s.Repo.Insert(qctx, s.Pool, row)
	if err != nil {
		return uuid.Nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: insert")
	}
	return id, nil
}

// GetByID delegates to the existing repo.
func (s *PgSubscriptionsStore) GetByID(ctx context.Context, id uuid.UUID) (*repos.SubscriptionRow, error) {
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	row, err := s.Repo.GetByID(qctx, s.Pool, id)
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: get by id")
	}
	return row, nil
}

// ListByClient queries subscriptions by client id. The existing repo
// has no equivalent; the small scope here justifies an inline query.
func (s *PgSubscriptionsStore) ListByClient(ctx context.Context, clientID string) ([]repos.SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, next_event_number,
		       COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE client_id = $1
		ORDER BY created_at DESC`
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	rows, err := s.Pool.Query(qctx, sql, clientID)
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: list by client")
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
			&rec.EventsSinceSubscriptionStart, &rec.NextEventNumber,
			&rec.Reason, &rec.EndTime,
			&rec.Error, &rec.Contact, &rec.LastHandshakeAt,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: scan")
		}
		rec.Status = repos.SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: list by client")
	}
	return out, nil
}

// FindByClientAndCriteria runs the If-None-Exist (LLD §4.1) match
// entirely in SQL. The composite index `subscriptions_client_match_idx`
// (migration 0005) covers the equality predicates so the query never
// materialises the full client subscription list (S-2.4). Tombstoned
// (`status = 'off'`) rows are excluded so the recreate-after-delete
// path is unblocked. LIMIT 1 keeps the round-trip flat — the caller
// only checks for presence.
func (s *PgSubscriptionsStore) FindByClientAndCriteria(ctx context.Context, clientID string, criteria SubscriptionMatchCriteria) ([]repos.SubscriptionRow, error) {
	const sql = `
		SELECT id, client_id, status, topic_url, channel_type,
		       COALESCE(endpoint, ''), header, filter_by, content,
		       heartbeat_period, timeout, max_count,
		       events_since_subscription_start, next_event_number,
		       COALESCE(reason, ''),
		       end_time, COALESCE(error, ''), contact, last_handshake_at,
		       created_at, updated_at
		FROM subscriptions
		WHERE client_id = $1
		  AND status <> 'off'
		  AND ($2 = '' OR topic_url = $2)
		  AND ($3 = '' OR channel_type = $3)
		  AND ($4 = '' OR COALESCE(endpoint, '') = $4)
		LIMIT 1`
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	rows, err := s.Pool.Query(qctx, sql, clientID, criteria.Topic, criteria.ChannelType, criteria.Endpoint)
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: find by criteria")
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
			&rec.EventsSinceSubscriptionStart, &rec.NextEventNumber,
			&rec.Reason, &rec.EndTime,
			&rec.Error, &rec.Contact, &rec.LastHandshakeAt,
			&rec.CreatedAt, &rec.UpdatedAt,
		); err != nil {
			return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: scan")
		}
		rec.Status = repos.SubscriptionStatus(status)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "subscriptions: find by criteria")
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
	qctx, cancel := s.Timeouts.withWrite(ctx)
	defer cancel()
	_, err := s.Pool.Exec(qctx, sql,
		id,
		row.TopicURL, row.ChannelType, row.Endpoint, row.Header, row.FilterBy,
		row.Content, row.HeartbeatPeriod, row.Timeout, row.MaxCount,
		row.Reason, row.EndTime, row.Contact,
	)
	if err != nil {
		return TranslateQueryErr(ctx, qctx, err, "subscriptions: update")
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
	qctx, cancel := s.Timeouts.withWrite(ctx)
	defer cancel()
	_, err := s.Pool.Exec(qctx, sql, id, string(status), errMsg)
	if err != nil {
		return TranslateQueryErr(ctx, qctx, err, "subscriptions: update status")
	}
	return nil
}

// HardDelete removes the subscription row entirely. The DELETE
// handler calls this to honor the FHIR R5 §3.4.4 DELETE contract:
// after a successful DELETE the resource MUST be gone (404 on
// re-read), not merely flipped to status=off. The audit_log row
// the handler writes is retained — its retention is governed by
// storage policy.
func (s *PgSubscriptionsStore) HardDelete(ctx context.Context, id uuid.UUID) error {
	const sql = `DELETE FROM subscriptions WHERE id = $1`
	qctx, cancel := s.Timeouts.withWrite(ctx)
	defer cancel()
	if _, err := s.Pool.Exec(qctx, sql, id); err != nil {
		return TranslateQueryErr(ctx, qctx, err, "subscriptions: hard-delete")
	}
	return nil
}

// PgTopicsStore wraps repos.SubscriptionTopicsRepo for ListActive.
type PgTopicsStore struct {
	Pool     *pgxpool.Pool
	Repo     *repos.SubscriptionTopicsRepo
	Timeouts QueryTimeouts
}

// NewPgTopicsStore is a convenience constructor with default timeouts.
func NewPgTopicsStore(pool *pgxpool.Pool) *PgTopicsStore {
	return NewPgTopicsStoreWithTimeouts(pool, QueryTimeouts{})
}

// NewPgTopicsStoreWithTimeouts lets the operator override per-query
// deadlines.
func NewPgTopicsStoreWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *PgTopicsStore {
	qt.ApplyDefaults()
	return &PgTopicsStore{Pool: pool, Repo: repos.NewSubscriptionTopicsRepo(), Timeouts: qt}
}

// ListActive returns all topic rows whose status='active'.
func (s *PgTopicsStore) ListActive(ctx context.Context) ([]repos.SubscriptionTopicRow, error) {
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	out, err := s.Repo.ListByStatus(qctx, s.Pool, "active")
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "topics: list active")
	}
	return out, nil
}

// PgDeadLettersStore wraps repos.DeadLettersRepo for the admin
// /admin/dead_letters surface. Mirrors the other Pg*Store adapters'
// pattern: hold a pool + repo, derive a read context, delegate to the
// repo, translate timeout errors to ErrQueryTimeout (story #92).
type PgDeadLettersStore struct {
	Pool     *pgxpool.Pool
	Repo     *repos.DeadLettersRepo
	Timeouts QueryTimeouts
}

// NewPgDeadLettersStore constructs the store with default timeouts.
func NewPgDeadLettersStore(pool *pgxpool.Pool, repo *repos.DeadLettersRepo) *PgDeadLettersStore {
	return NewPgDeadLettersStoreWithTimeouts(pool, repo, QueryTimeouts{})
}

// NewPgDeadLettersStoreWithTimeouts lets the operator override per-query
// deadlines.
func NewPgDeadLettersStoreWithTimeouts(pool *pgxpool.Pool, repo *repos.DeadLettersRepo, qt QueryTimeouts) *PgDeadLettersStore {
	qt.ApplyDefaults()
	return &PgDeadLettersStore{Pool: pool, Repo: repo, Timeouts: qt}
}

// ListRecent delegates to the existing repo.ListRecent under a bounded
// read context. The repo intentionally omits the encrypted
// payload_redacted blob; the admin handler also redacts it on the way
// out.
func (s *PgDeadLettersStore) ListRecent(ctx context.Context, limit int) ([]repos.DeadLetterRow, error) {
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	out, err := s.Repo.ListRecent(qctx, s.Pool, limit)
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "dead_letters: list recent")
	}
	return out, nil
}

// PgEventsStore implements EhrEventsStore for $events replay. We add a
// single read query directly to avoid touching the repo.
type PgEventsStore struct {
	Pool     *pgxpool.Pool
	Timeouts QueryTimeouts
}

// NewPgEventsStore constructs the store with default timeouts.
func NewPgEventsStore(pool *pgxpool.Pool) *PgEventsStore {
	return NewPgEventsStoreWithTimeouts(pool, QueryTimeouts{})
}

// NewPgEventsStoreWithTimeouts lets the operator override per-query
// deadlines.
func NewPgEventsStoreWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *PgEventsStore {
	qt.ApplyDefaults()
	return &PgEventsStore{Pool: pool, Timeouts: qt}
}

// PgDeliveriesStore implements DeliveriesStore for $status. The
// existing repo doesn't expose lastDeliveredEventNumber so we run the
// query inline here.
type PgDeliveriesStore struct {
	Pool     *pgxpool.Pool
	Timeouts QueryTimeouts
}

// NewPgDeliveriesStore constructs the store with default timeouts.
func NewPgDeliveriesStore(pool *pgxpool.Pool) *PgDeliveriesStore {
	return NewPgDeliveriesStoreWithTimeouts(pool, QueryTimeouts{})
}

// NewPgDeliveriesStoreWithTimeouts lets the operator override per-query
// deadlines.
func NewPgDeliveriesStoreWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *PgDeliveriesStore {
	qt.ApplyDefaults()
	return &PgDeliveriesStore{Pool: pool, Timeouts: qt}
}

// LastDeliveredEventNumber reads the highest event_number whose status
// is 'delivered' for the given subscription.
func (s *PgDeliveriesStore) LastDeliveredEventNumber(ctx context.Context, sub uuid.UUID) (int64, error) {
	const sql = `
		SELECT COALESCE(MAX(event_number), 0)
		FROM deliveries
		WHERE subscription_id = $1 AND status = 'delivered'`
	qctx, cancel := s.Timeouts.withRead(ctx)
	defer cancel()
	var n int64
	err := s.Pool.QueryRow(qctx, sql, sub).Scan(&n)
	if err != nil && err != pgx.ErrNoRows {
		return 0, TranslateQueryErr(ctx, qctx, err, "deliveries: last")
	}
	return n, nil
}

// PgWsBindingTokensStore wraps repos.WsBindingTokensRepo.
type PgWsBindingTokensStore struct {
	Pool     *pgxpool.Pool
	Repo     *repos.WsBindingTokensRepo
	Timeouts QueryTimeouts
}

// NewPgWsBindingTokensStore is a convenience constructor with default
// timeouts.
func NewPgWsBindingTokensStore(pool *pgxpool.Pool) *PgWsBindingTokensStore {
	return NewPgWsBindingTokensStoreWithTimeouts(pool, QueryTimeouts{})
}

// NewPgWsBindingTokensStoreWithTimeouts lets the operator override
// per-query deadlines.
func NewPgWsBindingTokensStoreWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *PgWsBindingTokensStore {
	qt.ApplyDefaults()
	return &PgWsBindingTokensStore{Pool: pool, Repo: repos.NewWsBindingTokensRepo(), Timeouts: qt}
}

// Insert delegates to the existing repo.
func (s *PgWsBindingTokensStore) Insert(ctx context.Context, row repos.WsBindingTokenRow) error {
	qctx, cancel := s.Timeouts.withWrite(ctx)
	defer cancel()
	if err := s.Repo.Insert(qctx, s.Pool, row); err != nil {
		return TranslateQueryErr(ctx, qctx, err, "ws_binding_tokens: insert")
	}
	return nil
}

// ChainedAuditStore adapts an *audit.Writer to the handlers.AuditStore
// contract. It is the production wiring slot for Deps.Audit (story #105):
// every API handler call to Deps.Audit.Append flows into the same
// audit.Writer that observability.Start manages, so the on-disk
// audit_log chain is a single linear sequence and `fhir-subs audit
// verify` can walk it end-to-end.
//
// The handler-side AuditStore signature carries (action, target,
// outcome, correlationID, canonical-body bytes); the writer wants a
// full audit.Event. The adapter assembles the Event from those args:
// ActorKind defaults to "subscriber" because the API surfaces are
// subscriber-driven (admin paths use a different audit emit), and the
// canonical body becomes the Payload's "body" key so it lands in the
// audit_log.payload JSONB column intact.
type ChainedAuditStore struct {
	w     *audit.Writer
	now   func() time.Time
	actor string
}

// NewChainedAuditStore wraps w. Subsequent Append calls flow through
// the writer's Emit, picking up the chain advisory lock and the
// canonical/chain-hash bookkeeping that the audit package owns.
func NewChainedAuditStore(w *audit.Writer) *ChainedAuditStore {
	return &ChainedAuditStore{w: w, now: time.Now, actor: "subscriber"}
}

// Append assembles an audit.Event from the handler-side arguments and
// forwards to the writer. A nil correlationID is forwarded as the zero
// UUID; the chain reflects the handler's actual correlation state and
// the pg-backed store persists it verbatim (story #107).
func (s *ChainedAuditStore) Append(ctx context.Context, action, target, outcome string, correlationID *uuid.UUID, canonical []byte) error {
	var cid uuid.UUID
	if correlationID != nil {
		cid = *correlationID
	}
	payload := map[string]any{}
	if len(canonical) > 0 {
		payload["body"] = string(canonical)
	}
	evt := audit.Event{
		OccurredAt:    s.now().UTC(),
		ActorKind:     s.actor,
		Action:        action,
		TargetKind:    "Subscription",
		TargetID:      target,
		Outcome:       outcome,
		CorrelationID: cid,
		Payload:       payload,
	}
	return s.w.Emit(ctx, evt)
}

// AuthClientLookup wraps repos.AuthClientsRepo so the Verifier can
// resolve client records from the auth_clients table.
type AuthClientLookup struct {
	Pool     *pgxpool.Pool
	Repo     *repos.AuthClientsRepo
	Timeouts QueryTimeouts
}

// NewAuthClientLookup constructs the lookup adapter with default
// timeouts.
func NewAuthClientLookup(pool *pgxpool.Pool) *AuthClientLookup {
	return NewAuthClientLookupWithTimeouts(pool, QueryTimeouts{})
}

// NewAuthClientLookupWithTimeouts lets the operator override per-query
// deadlines.
func NewAuthClientLookupWithTimeouts(pool *pgxpool.Pool, qt QueryTimeouts) *AuthClientLookup {
	qt.ApplyDefaults()
	return &AuthClientLookup{Pool: pool, Repo: repos.NewAuthClientsRepo(), Timeouts: qt}
}

// GetByID delegates to the existing repo.
func (a *AuthClientLookup) GetByID(ctx context.Context, id string) (*auth.ClientRecord, error) {
	qctx, cancel := a.Timeouts.withRead(ctx)
	defer cancel()
	row, err := a.Repo.GetByID(qctx, a.Pool, id)
	if err != nil {
		return nil, TranslateQueryErr(ctx, qctx, err, "auth_clients: get by id")
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
		       events_since_subscription_start, next_event_number,
		       COALESCE(reason, ''),
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
			&rec.EventsSinceSubscriptionStart, &rec.NextEventNumber,
			&rec.Reason, &rec.EndTime,
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

// ListByTopicAndRangePage returns ehr_events rows for the given
// (topic_url, client_id) pair. clientID is the authenticated caller's
// tenant id; the predicate enforces tenant isolation in the event log
// (OP #274). Passing the empty string is treated as "no events" so a
// missing principal cannot accidentally bypass the filter.
func (s *PgEventsStore) ListByTopicAndRangePage(ctx context.Context, topicURL, clientID string, since, until int64, limit int) ([]repos.EhrEventRow, error) {
	if clientID == "" {
		return []repos.EhrEventRow{}, nil
	}
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
		SELECT id, event_number, client_id, topic_url, focus, change_kind, occurred_at,
		       resource_change_id
		FROM ehr_events
		WHERE topic_url = $1
		  AND client_id = $2
		  AND ($3 = 0 OR event_number >= $3)
		  AND ($4 = 0 OR event_number <= $4)
		ORDER BY event_number ASC
		LIMIT $5`
	//nolint:gosec // limit is bounded above by maxEventReplayPageSize which fits int32
	limit32 := int32(limit)
	rows, err := s.Pool.Query(ctx, sql, topicURL, clientID, since, until, limit32)
	if err != nil {
		return nil, fmt.Errorf("ehr_events: replay page: %w", err)
	}
	defer rows.Close()
	out := make([]repos.EhrEventRow, 0)
	for rows.Next() {
		var rec repos.EhrEventRow
		var kind string
		if err := rows.Scan(
			&rec.ID, &rec.EventNumber, &rec.ClientID, &rec.TopicURL, &rec.Focus, &kind,
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
