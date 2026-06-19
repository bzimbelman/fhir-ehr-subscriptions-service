// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package channel defines the notification-channel SPI: the contract every
// outbound delivery channel implements. A channel turns a NotificationEnvelope
// (the engine's pre-built notification bundle plus per-subscription metadata)
// into protocol bytes on the wire and reports a DeliveryOutcome.
//
// Channels do NOT own:
//   - cross-attempt retry policy (the delivery scheduler does);
//   - bundle assembly (Stage 4 / Notification Builder does);
//   - per-subscription filtering (Stage 3 does);
//   - subscription state transitions (the engine does).
//
// Channels DO own:
//   - protocol-level timeouts and in-attempt connection retries;
//   - failure classification into Delivered / TransientFailure / PermanentFailure;
//   - protocol-specific metadata (HTTP headers, MIME composition, frame encoding).
//
// See docs/low-level-design/channels.md for the full contract.
package channel

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// BundleKind tags the notification type per the FHIR R5
// SubscriptionStatus.type code system.
type BundleKind string

// BundleKind values.
const (
	BundleEventNotification BundleKind = "event-notification"
	BundleHeartbeat         BundleKind = "heartbeat"
	BundleHandshake         BundleKind = "handshake"
	BundleQueryStatus       BundleKind = "query-status"
	BundleQueryEvent        BundleKind = "query-event"
)

// PayloadType is the Subscription.content shape the subscriber asked for.
type PayloadType string

// PayloadType values.
const (
	PayloadEmpty        PayloadType = "empty"
	PayloadIDOnly       PayloadType = "id-only"
	PayloadFullResource PayloadType = "full-resource"
)

// ContentType is the wire content-type the bundle is serialized as.
type ContentType string

// ContentType values.
const (
	ContentTypeFHIRJSON ContentType = "application/fhir+json"
	ContentTypeFHIRXML  ContentType = "application/fhir+xml"
)

// Param is one Subscription.parameter[] entry.
type Param struct {
	Name  string
	Value string
}

// NotificationEnvelope is the read-only payload the scheduler hands a
// channel for one delivery attempt.
type NotificationEnvelope struct {
	SubscriptionID         uuid.UUID
	Sequence               uint64 // eventsSinceSubscriptionStart
	BundleBytes            []byte // already serialized in ContentType
	BundleKind             BundleKind
	PayloadType            PayloadType
	ContentType            ContentType
	Attempt                uint32 // 0 for first delivery; increments on retry
	CorrelationID          string
	SubscriptionEndpoint   string
	SubscriptionParameters []Param
	Deadline               time.Time // hard wall-clock deadline for this attempt
}

// OutcomeKind is the closed enum tag on DeliveryOutcome.
type OutcomeKind int

// OutcomeKind values.
const (
	// OutcomeDelivered means the subscriber accepted the bundle.
	OutcomeDelivered OutcomeKind = iota
	// OutcomeTransient means the failure is retryable and the scheduler
	// should reschedule. RetryAfter, if non-zero, is a floor hint.
	OutcomeTransient
	// OutcomePermanent means the failure is terminal for this delivery
	// and the scheduler should dead-letter without retry.
	OutcomePermanent
)

// DeliveryOutcome is the closed-enum result of one delivery attempt.
type DeliveryOutcome struct {
	Kind       OutcomeKind
	Reason     string
	RetryAfter time.Duration // populated only on Transient; zero means no hint
	StatusCode int           // protocol status (HTTP code, SMTP code, etc.); 0 if not applicable
}

// Delivered constructs a successful outcome.
func Delivered() DeliveryOutcome {
	return DeliveryOutcome{Kind: OutcomeDelivered}
}

// TransientFailure constructs a retryable failure.
func TransientFailure(retryAfter time.Duration, reason string) DeliveryOutcome {
	return DeliveryOutcome{Kind: OutcomeTransient, Reason: reason, RetryAfter: retryAfter}
}

// PermanentFailure constructs a terminal failure.
func PermanentFailure(reason string) DeliveryOutcome {
	return DeliveryOutcome{Kind: OutcomePermanent, Reason: reason}
}

// Channel is the SPI every notification channel implements.
//
// Implementations MUST be safe to call concurrently from multiple
// goroutines: the scheduler may dispatch many deliveries in parallel.
type Channel interface {
	// Deliver sends the envelope to its subscriber and returns the outcome.
	// An error result is reserved for setup-time problems before any
	// network I/O (invalid envelope, channel not started); transport
	// failures classified by the channel return a DeliveryOutcome with
	// nil error.
	Deliver(ctx context.Context, env NotificationEnvelope) (DeliveryOutcome, error)

	// Close releases channel-owned resources (HTTP transports, websocket
	// sessions, SMTP pools). The lifecycle module calls Close on every
	// registered channel during graceful shutdown so in-flight bind
	// handshakes and pooled connections drain deterministically. Close
	// MUST be idempotent — multiple calls return nil after the first.
	Close() error
}

// MetricsEmitter is the metrics seam between channel modules and the host's
// observability stack. Mirrors internal/mllp.MetricsEmitter; the channel
// emits typed events with stable label sets and the host adapts to its
// chosen sink.
type MetricsEmitter interface {
	Inc(name string, labels map[string]string)
	Add(name string, delta float64, labels map[string]string)
	Observe(name string, value float64, labels map[string]string)
	Set(name string, value float64, labels map[string]string)
}

// NopMetrics is a no-op MetricsEmitter for tests and channels that do
// not yet wire their host's metrics.
type NopMetrics struct{}

// Inc is a no-op.
func (NopMetrics) Inc(string, map[string]string) {}

// Add is a no-op.
func (NopMetrics) Add(string, float64, map[string]string) {}

// Observe is a no-op.
func (NopMetrics) Observe(string, float64, map[string]string) {}

// Set is a no-op.
func (NopMetrics) Set(string, float64, map[string]string) {}
