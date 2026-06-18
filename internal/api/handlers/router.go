// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/fhirerror"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// HandshakeOutcome is the result of a channel module's per-subscription
// activation handshake.
type HandshakeOutcome string

// HandshakeOutcome values.
const (
	HandshakeSucceeded HandshakeOutcome = "succeeded"
	HandshakeFailed    HandshakeOutcome = "failed"
)

// ChannelActivator is the narrow interface the API needs from a channel
// module. The full Channel SPI is owned by the channels package; the
// API touches only on_subscription_activated.
type ChannelActivator interface {
	ActivateSubscription(ctx context.Context, sub repos.SubscriptionRow) (HandshakeOutcome, error)
}

// ChannelRegistry maps channel-type code (e.g., "rest-hook",
// "websocket") to its activator.
type ChannelRegistry map[string]ChannelActivator

// MetricsRecorder is the narrow surface handlers use to record
// subscription, ws-binding-token, and validation metrics. The API
// metrics package provides the canonical implementation; tests can
// inject an in-memory recorder. Nil is permitted everywhere.
type MetricsRecorder interface {
	RecordSubscriptionCreated()
	RecordSubscriptionUpdated()
	RecordSubscriptionDeleted()
	RecordWSBindingTokenIssued()
	RecordValidationFailure(kind string)
}

// Deps is the bundle of dependencies the handlers need at request time.
type Deps struct {
	Subscriptions SubscriptionsStore
	Topics        SubscriptionTopicsStore
	Events        EhrEventsStore
	Deliveries    DeliveriesStore
	WsTokens      WsBindingTokensStore
	Audit         AuditStore
	Channels      ChannelRegistry

	// Metrics is the optional recorder used to emit per-action
	// subscription / ws / validation metrics. Nil is fine.
	Metrics MetricsRecorder

	// Now returns the current time. Tests substitute a fixed value.
	Now func() time.Time

	// WSBindingTTL is the lifetime of issued ws-binding tokens.
	WSBindingTTL time.Duration

	// BaseURL is the public base URL of this server. Used to build
	// CapabilityStatement and absolute URLs.
	BaseURL string

	// WSBaseURL is the public WSS URL prefix used in
	// $get-ws-binding-token responses (e.g., "wss://api.example/ws").
	WSBaseURL string

	// ServerVersion is rendered into CapabilityStatement.software.
	ServerVersion string
}

// RegisterRoutes wires every handler onto r. Auth middleware MUST be
// installed upstream of these routes; the handlers depend on the
// principal being present in the context.
func RegisterRoutes(r chi.Router, d Deps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.WSBindingTTL == 0 {
		d.WSBindingTTL = 5 * time.Minute
	}

	h := &server{deps: d}

	r.Route("/Subscription", func(r chi.Router) {
		r.Post("/", h.createSubscription)
		r.Get("/", h.searchSubscriptions)
		r.Get("/{id}", h.readSubscription)
		r.Put("/{id}", h.updateSubscription)
		r.Delete("/{id}", h.deleteSubscription)
		r.Get("/{id}/$status", h.opStatusSingle)
		r.Get("/$status", h.opStatusBulk)
		r.Get("/{id}/$events", h.opEvents)
		r.Post("/{id}/$get-ws-binding-token", h.opGetWsBindingToken)
	})

	r.Get("/SubscriptionTopic", h.searchTopics)
	r.Get("/SubscriptionTopic/{id}", h.readTopic)
	r.Get("/metadata", h.getCapabilityStatement)

	// Catch-all: every unknown route returns an OperationOutcome 404.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		fhirerror.WriteError(w, http.StatusNotFound, fhirerror.CodeNotFound, "no such endpoint")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		fhirerror.WriteError(w, http.StatusMethodNotAllowed, fhirerror.CodeNotSupported, "method not allowed")
	})
}

type server struct {
	deps Deps
}
