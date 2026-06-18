// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package message implements the FHIR messaging notification channel:
// HTTPS POST of a FHIR Bundle of type "message" that wraps the
// subscription-notification Bundle in a MessageHeader resource.
//
// Stub during RED phase — Deliver returns a Transient failure; the
// implementation lands in the GREEN commit.
package message

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
)

// Default values mirror the rest-hook defaults per LLD §4.4 (delegates to
// rest-hook defaults where unset).
const (
	DefaultUserAgent       = "fhir-subscriptions-foss/0.0"
	DefaultRequestTimeout  = 30 * time.Second
	DefaultMaxResponseBody = 256
)

// Metric names emitted by this channel. Per LLD §9 the message channel
// shares the rest-hook status metric and adds a wrapping-duration histogram.
const (
	MetricDeliveriesTotal       = "fhir_subs_channel_message_deliveries_total"
	MetricDeliveryDurationSec   = "fhir_subs_channel_message_delivery_duration_seconds"
	MetricStatusTotal           = "fhir_subs_channel_message_status_total"
	MetricHeadersFilteredTotal  = "fhir_subs_channel_message_headers_filtered_total"
	MetricNonHTTPSEndpointTotal = "fhir_subs_channel_message_non_https_endpoint_total"
	MetricWrappingDurationSec   = "fhir_subs_channel_message_wrapping_duration_seconds"
	MetricWrapFailureTotal      = "fhir_subs_channel_message_wrap_failures_total"
)

// Options configures a message Channel at construction time. Zero values
// fall back to package defaults.
type Options struct {
	HTTPClient     *http.Client
	Metrics        channel.MetricsEmitter
	Logger         *slog.Logger
	UserAgent      string
	RequestTimeout time.Duration
	// ServerEndpoint is the URI placed in MessageHeader.source.endpoint.
	// Per LLD §4.4 this is "the server's identity URI
	// (config.deployment.facility_id lifted into a URI)". Empty is allowed
	// (the field is omitted from the wrapped MessageHeader).
	ServerEndpoint string
}

// Channel implements the FHIR messaging delivery channel. Construct with New.
type Channel struct {
	// stub — fields land in GREEN.
}

// New constructs a message Channel.
func New(_ Options) (*Channel, error) {
	return &Channel{}, nil
}

// Deliver — stub. Returns a sentinel transient failure until the
// implementation lands. The signature matches channel.Channel so tests
// compile and fail with a meaningful outcome rather than a panic.
func (c *Channel) Deliver(_ context.Context, _ channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	return channel.TransientFailure(0, "message channel not yet implemented"),
		errors.New("message channel not yet implemented")
}

// compile-time assertion mirrors the resthook package.
var _ channel.Channel = (*Channel)(nil)
