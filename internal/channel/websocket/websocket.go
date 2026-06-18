// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package websocket implements the FHIR R5 WebSocket notification
// channel. The channel runs a server-side WSS endpoint subscribers connect
// to after acquiring a single-use binding token via $get-ws-binding-token,
// then writes subscription-notification Bundle frames over the socket.
//
// See docs/low-level-design/channels.md §4.2 for the contract.
package websocket

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
)

// ConsumeOutcome and ConsumeResult mirror
// internal/infra/storage/repos.ConsumeOutcome so the websocket package can
// take a small interface dependency rather than a concrete repo type.
// Production wiring adapts the repo into TokenConsumer.

// ConsumeOutcome reports the result of a single-use token redemption.
type ConsumeOutcome int

// ConsumeOutcome values.
const (
	// ConsumeOK means the token was redeemed and the bound subscription is
	// returned in ConsumeResult.SubscriptionID.
	ConsumeOK ConsumeOutcome = iota
	// ConsumeNotFound means no row exists for the supplied token.
	ConsumeNotFound
	// ConsumeAlreadyUsed means the token has been redeemed previously.
	ConsumeAlreadyUsed
	// ConsumeExpired means the token's expires_at is in the past.
	ConsumeExpired
)

// ConsumeResult is the typed result of a Consume call.
type ConsumeResult struct {
	Outcome        ConsumeOutcome
	SubscriptionID uuid.UUID
	ClientID       string
}

// TokenConsumer is the seam to the ws_binding_tokens repository. It is the
// only state the channel needs from storage to admit a connection: a
// single-use, atomically-redeemable lookup of the bind token.
type TokenConsumer interface {
	Consume(ctx context.Context, token string, now time.Time) (ConsumeResult, error)
}

// PastEvent is one previously-emitted notification a subscriber missed
// while disconnected. The channel replays these on reconnect when the
// bind message includes lastReceivedEventNumber.
type PastEvent struct {
	EventNumber uint64
	Bundle      []byte
	ContentType channel.ContentType
}

// EventReplayer fetches missed events for replay on reconnect. The
// implementation is engine-owned (it walks deliveries / ehr_events for the
// subscription); the channel takes only this minimal interface so it
// stays confined to its own package.
type EventReplayer interface {
	ReplaySince(ctx context.Context, subscriptionID uuid.UUID, after uint64) ([]PastEvent, error)
}

// Options configures a Channel.
type Options struct {
	// Tokens is the single-use bind-token redemption seam (required).
	Tokens TokenConsumer
	// Replayer is the missed-event replay seam (required).
	Replayer EventReplayer
	// Now returns the current time. nil falls back to time.Now.
	Now func() time.Time
	// Metrics receives counter increments. nil uses channel.NopMetrics.
	Metrics channel.MetricsEmitter
	// PingInterval is the WSS ping cadence. <= 0 falls back to 30s.
	PingInterval time.Duration
	// IdleTimeout is the no-traffic close threshold. <= 0 falls back to 5m.
	IdleTimeout time.Duration
	// MaxFrameBytes caps an outbound frame size. <= 0 falls back to 8 MiB.
	MaxFrameBytes int
	// TransientRetryAfter is the floor hint returned to the scheduler when
	// a delivery arrives without a bound socket. <= 0 falls back to 30s.
	TransientRetryAfter time.Duration
	// AckTimeout caps how long Deliver waits for the subscriber's ack of
	// an emitted frame. <= 0 falls back to 30s. Independent of the
	// envelope deadline so a slow ack does not silently classify as
	// success past the scheduler's expectation.
	AckTimeout time.Duration
}

// Channel is the websocket notification channel. Construct with New.
// Channel implements internal/channel.Channel and exposes an http.Handler
// that subscribers connect to.
type Channel struct {
	// fields filled in by GREEN implementation
}

// New constructs a Channel.
func New(opts Options) (*Channel, error) {
	_ = opts
	return nil, errors.New("websocket: New unimplemented")
}

// Handler returns the http.Handler that accepts WSS upgrade requests at
// /ws/subscriptions and binds the connection to a subscription via the
// bind handshake described in docs/low-level-design/channels.md §4.2.
func (c *Channel) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "websocket: Handler unimplemented", http.StatusNotImplemented)
	})
}

// Deliver implements channel.Channel: send the envelope's bundle to the
// bound subscriber and report a DeliveryOutcome.
func (c *Channel) Deliver(ctx context.Context, env channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	_ = ctx
	_ = env
	return channel.DeliveryOutcome{}, errors.New("websocket: Deliver unimplemented")
}

// Close stops the channel and disconnects bound subscribers.
func (c *Channel) Close() error {
	return nil
}
