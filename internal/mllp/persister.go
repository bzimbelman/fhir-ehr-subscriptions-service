// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// QueueRow is the row shape persisted to hl7_message_queue. The listener
// owns construction of this value: it generates the row id and the
// correlation_id (UUIDv4 per ADR 0010 §1), captures the wire metadata,
// and copies the verbatim inter-marker bytes into Body.
//
// Fields map 1:1 to the columns defined in migrations/0001_init.sql:
//
//	id               -> uuid not null
//	received_at      -> timestamptz not null
//	listener_endpoint-> text not null
//	peer_addr        -> text not null
//	mllp_message_id  -> text (nullable; empty string when MSH-10 absent)
//	correlation_id   -> uuid not null
//	raw_body         -> bytea not null  (called Body here; same bytes)
type QueueRow struct {
	ID               uuid.UUID
	ReceivedAt       time.Time
	ListenerEndpoint string
	PeerAddr         string
	MLLPMessageID    string
	CorrelationID    uuid.UUID
	Body             []byte
}

// Persister is the storage seam between the listener and the rest of the
// service. The listener writes one row per successfully framed message via
// Persist. The storage repo (in a future commit, outside this package) will
// satisfy this interface; tests use a fake.
//
// Per LLD section 5.6, Persist must run synchronously and return only after
// the Postgres COMMIT is durable. A non-nil error means no row was committed
// and the listener will NACK or drop the connection.
type Persister interface {
	// Persist writes the row to hl7_message_queue and commits. The context
	// carries the per-message persist timeout from MllpListenerConfig.
	Persist(ctx context.Context, row QueueRow) error
}

// Sentinel errors a Persister implementation may return to signal whether
// the listener should treat the failure as transient (count toward the
// drop threshold) or permanent (log at error and surface to operators).
//
// A Persister that returns a bare error is treated as Transient by default,
// matching the LLD's "default to retry, surface if permanent" disposition.
var (
	// ErrPersistTransient signals a retryable persistence failure: pool
	// exhaustion, statement timeout, or a Postgres error class the
	// adapter classifies as retryable.
	ErrPersistTransient = errors.New("persist transient failure")

	// ErrPersistPermanent signals a non-retryable persistence failure:
	// integrity violation, schema drift, or a programmer error.
	ErrPersistPermanent = errors.New("persist permanent failure")
)
