// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Querier is the minimal pgx interface every storage call needs. It
// matches repos.Querier so the existing repos accept it directly.
type Querier = repos.Querier

// SubscriptionCursor is the opaque-by-design forward keyset cursor used
// by ListByClientPage. It is sized so the encoded form is short enough
// to fit comfortably in a URL.
//
// CreatedAt + ID together form a strict total order over `subscriptions`
// (id is unique). The page query selects rows strictly older than the
// (CreatedAt, ID) pair so consecutive pages never overlap and a row
// inserted between pages is observed at most once on the cursor's
// sort axis.
type SubscriptionCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// SubscriptionsStore is the narrow interface the handlers need from the
// subscriptions table. It is satisfied by an adapter wrapping the pgx
// pool plus repos.SubscriptionsRepo and a few extra queries
// (status/cursor updates and listing by client) that are not part of
// the existing repo's surface and are implemented directly against the
// table here in the API package.
//
// ListByClientPage returns up to limit rows ordered by created_at DESC,
// id DESC. A nil after means "from the start". The returned slice has
// at most limit rows; callers detect end-of-results by getting a slice
// shorter than limit (or by re-querying with the last row's cursor and
// receiving zero rows).
type SubscriptionsStore interface {
	Insert(ctx context.Context, row repos.SubscriptionRow) (uuid.UUID, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repos.SubscriptionRow, error)
	ListByClient(ctx context.Context, clientID string) ([]repos.SubscriptionRow, error)
	ListByClientPage(ctx context.Context, clientID string, after *SubscriptionCursor, limit int) ([]repos.SubscriptionRow, error)
	// FindByClientAndCriteria runs the If-None-Exist (LLD §4.1) match in
	// SQL: every supplied predicate (topic / channel type / endpoint) is
	// folded into the WHERE clause along with `client_id = $1` and
	// `status <> 'off'`. The database returns at most one row even when
	// many subscriptions exist for the client (S-2.4 — predicate is
	// pushed into the index instead of materializing the entire client
	// list in Go).
	FindByClientAndCriteria(ctx context.Context, clientID string, criteria SubscriptionMatchCriteria) ([]repos.SubscriptionRow, error)
	UpdateResource(ctx context.Context, id uuid.UUID, row repos.SubscriptionRow) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error
}

// SubscriptionMatchCriteria carries the LLD §4.1 search parameters that
// participate in the If-None-Exist evaluation. Empty fields mean "do
// not constrain on this column" so the same struct can express any
// subset the client supplied. All comparisons are strict equality
// (FHIR-search `:exact` semantics) — case-folding lives one layer above
// in the parser when the underlying field is case-insensitive.
type SubscriptionMatchCriteria struct {
	Topic       string
	ChannelType string
	Endpoint    string
}

// AuthClientsStore is the narrow interface the handlers need from
// auth_clients.
type AuthClientsStore interface {
	GetByID(ctx context.Context, id string) (*repos.AuthClientRow, error)
}

// SubscriptionTopicsStore is the narrow interface for topic catalog
// lookup. ListActive returns all rows with status='active'; the
// handlers select by URL in-memory because the catalog is small.
type SubscriptionTopicsStore interface {
	ListActive(ctx context.Context) ([]repos.SubscriptionTopicRow, error)
}

// EhrEventsStore is the narrow read-only interface for $events replay.
//
// ListByTopicAndRangePage replaces the legacy hardcoded LIMIT 1000 path
// with an explicit operator-controlled page size. The handler asks for
// limit+1 rows so it can detect truncation: if the store returns more
// than limit rows the handler trims the slice and emits a Bundle.link
// `next` pointing the client at eventsSinceNumber=lastEventNumber+1.
// limit <= 0 means "no cap"; production callers always pass a positive
// value.
type EhrEventsStore interface {
	ListByTopicAndRange(ctx context.Context, topicURL string, since, until int64) ([]repos.EhrEventRow, error)
	ListByTopicAndRangePage(ctx context.Context, topicURL string, since, until int64, limit int) ([]repos.EhrEventRow, error)
}

// DeliveriesStore is the narrow read-only interface for $status.
type DeliveriesStore interface {
	LastDeliveredEventNumber(ctx context.Context, subscriptionID uuid.UUID) (int64, error)
}

// WsBindingTokensStore is the narrow interface for $get-ws-binding-token.
type WsBindingTokensStore interface {
	Insert(ctx context.Context, row repos.WsBindingTokenRow) error
}

// AuditStore writes one audit_log row. Audit hash chaining is delegated
// to the observability/audit module; the API just supplies the event.
type AuditStore interface {
	Append(ctx context.Context, action, target, outcome string, correlationID *uuid.UUID, canonical []byte) error
}
