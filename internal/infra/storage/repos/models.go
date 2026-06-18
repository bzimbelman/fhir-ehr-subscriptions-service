// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package repos exposes the typed repository interfaces and row models
// for every project-internal table. SQL is encapsulated here; nothing
// outside the storage module issues raw queries.
package repos

import (
	"time"

	"github.com/google/uuid"
)

// ChangeKind is the FHIR-shaped change type carried on resource_changes
// and ehr_events.
type ChangeKind string

// ChangeKind values.
const (
	ChangeCreate ChangeKind = "create"
	ChangeUpdate ChangeKind = "update"
	ChangeDelete ChangeKind = "delete"
)

// PendingKind is the kind of half-pair held in pending_pairs.
type PendingKind string

// PendingKind values.
const (
	PendingDelete PendingKind = "delete"
	PendingCreate PendingKind = "create"
)

// DeliveryStatus is the deliveries.status enum.
type DeliveryStatus string

// DeliveryStatus values. The migration uses the v0 status set; we keep
// them aligned. Note: 0001_init.sql uses 'failed' / 'dead'; the LLD
// uses 'failed_transient' / 'failed_permanent'. We use the v0 set as
// authoritative because the schema is already cut.
const (
	DeliveryPending    DeliveryStatus = "pending"
	DeliveryDelivering DeliveryStatus = "delivering"
	DeliveryDelivered  DeliveryStatus = "delivered"
	DeliveryFailed     DeliveryStatus = "failed"
	DeliveryDead       DeliveryStatus = "dead"
)

// SubscriptionStatus is the subscriptions.status enum.
type SubscriptionStatus string

// SubscriptionStatus values.
const (
	SubRequested      SubscriptionStatus = "requested"
	SubActive         SubscriptionStatus = "active"
	SubError          SubscriptionStatus = "error"
	SubOff            SubscriptionStatus = "off"
	SubEnteredInError SubscriptionStatus = "entered-in-error"
)

// Hl7MessageQueueRow mirrors hl7_message_queue. raw_body is decrypted
// to plaintext at the repo boundary (the column is bytea + key_version).
type Hl7MessageQueueRow struct {
	ID               uuid.UUID
	ListenerEndpoint string
	PeerAddr         string
	ReceivedAt       time.Time
	MllpMessageID    string
	CorrelationID    uuid.UUID
	Processed        bool
	ProcessedAt      *time.Time
	RawBody          []byte // plaintext after decryption
	KeyVersion       int32
}

// ResourceChangeRow mirrors resource_changes.
type ResourceChangeRow struct {
	ID               uuid.UUID
	Sequence         int64
	AdapterID        string
	CorrelationID    uuid.UUID
	ResourceType     string
	ChangeKind       ChangeKind
	Resource         []byte
	PreviousResource []byte
	KeyVersion       int32
	OccurredAt       time.Time
	EventCode        string
	Processed        bool
	CreatedMonth     time.Time
	CreatedAt        time.Time
}

// EhrEventRow mirrors ehr_events.
type EhrEventRow struct {
	ID                    uuid.UUID
	EventNumber           int64
	TopicURL              string
	Focus                 string
	ChangeKind            ChangeKind
	Resource              []byte
	PreviousResource      []byte
	KeyVersion            int32
	CorrelationID         uuid.UUID
	OccurredAt            time.Time
	NotificationShapeHint []byte
	ResourceChangeID      uuid.UUID
	Processed             bool
	ProcessedAt           *time.Time
	CreatedMonth          time.Time
	CreatedAt             time.Time
}

// DeliveryRow mirrors deliveries.
type DeliveryRow struct {
	ID             uuid.UUID
	SubscriptionID uuid.UUID
	EhrEventID     uuid.UUID
	EventNumber    int64
	Status         DeliveryStatus
	Attempts       int32
	NextAttemptAt  time.Time
	LastError      string
	Bundle         []byte
	KeyVersion     int32
	CorrelationID  uuid.UUID
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// DeadLetterRow mirrors dead_letters.
type DeadLetterRow struct {
	ID              uuid.UUID
	Kind            string
	SourceTable     string
	SourceID        uuid.UUID
	SubscriptionID  *uuid.UUID
	Reason          string
	ErrorDetail     []byte
	PayloadRedacted []byte
	CorrelationID   *uuid.UUID
	CreatedAt       time.Time
}

// PendingPairRow mirrors pending_pairs.
type PendingPairRow struct {
	CorrelationKey   string
	ListenerEndpoint string
	PendingResource  []byte
	PendingKind      PendingKind
	SourceMessageID  uuid.UUID
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// AdapterStateRow mirrors adapter_state.
type AdapterStateRow struct {
	AdapterID  string
	Scope      string
	Key        string
	Value      []byte
	KeyVersion int32
	UpdatedAt  time.Time
}

// SubscriptionRow mirrors subscriptions.
type SubscriptionRow struct {
	ID                           uuid.UUID
	ClientID                     string
	Status                       SubscriptionStatus
	TopicURL                     string
	ChannelType                  string
	Endpoint                     string
	Header                       []byte
	FilterBy                     []byte
	Content                      string
	HeartbeatPeriod              *time.Duration
	Timeout                      *time.Duration
	MaxCount                     int32
	EventsSinceSubscriptionStart int64
	Reason                       string
	EndTime                      *time.Time
	Error                        string
	Contact                      []byte
	LastHandshakeAt              *time.Time
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

// SubscriptionTopicRow mirrors subscription_topics.
type SubscriptionTopicRow struct {
	ID           uuid.UUID
	URL          string
	Version      string
	Title        string
	Description  string
	Status       string
	Date         *time.Time
	Source       string
	Body         []byte
	CompiledForm []byte
	CreatedAt    time.Time
	RetiredAt    *time.Time
}

// AuthClientRow mirrors auth_clients.
type AuthClientRow struct {
	ID          string
	JwksURL     string
	Scopes      []string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// WsBindingTokenRow mirrors ws_binding_tokens.
type WsBindingTokenRow struct {
	Token          string
	SubscriptionID uuid.UUID
	ClientID       string
	ExpiresAt      time.Time
	CreatedAt      time.Time
}

// AuditLogRow mirrors audit_log.
type AuditLogRow struct {
	Seq           int64
	OccurredAt    time.Time
	ActorKind     string
	ActorID       string
	Action        string
	TargetKind    string
	TargetID      string
	Outcome       string
	CorrelationID *uuid.UUID
	CanonicalForm []byte
	Hash          []byte
	PrevHash      []byte
}
