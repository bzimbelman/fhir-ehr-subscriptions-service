// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Querier is the minimal pgx interface every storage call needs. It
// matches repos.Querier so the existing repos accept it directly.
type Querier = repos.Querier

// SubscriptionsStore is the narrow interface the handlers need from the
// subscriptions table. It is satisfied by an adapter wrapping the pgx
// pool plus repos.SubscriptionsRepo and a few extra queries
// (status/cursor updates and listing by client) that are not part of
// the existing repo's surface and are implemented directly against the
// table here in the API package.
type SubscriptionsStore interface {
	Insert(ctx context.Context, row repos.SubscriptionRow) (uuid.UUID, error)
	GetByID(ctx context.Context, id uuid.UUID) (*repos.SubscriptionRow, error)
	ListByClient(ctx context.Context, clientID string) ([]repos.SubscriptionRow, error)
	UpdateResource(ctx context.Context, id uuid.UUID, row repos.SubscriptionRow) error
	UpdateStatus(ctx context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error
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
type EhrEventsStore interface {
	ListByTopicAndRange(ctx context.Context, topicURL string, since, until int64) ([]repos.EhrEventRow, error)
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
