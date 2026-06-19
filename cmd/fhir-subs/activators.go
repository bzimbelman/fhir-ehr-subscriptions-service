// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// restHookActivatorOptions parameterizes newRestHookActivator. Operators
// configure these via channels.rest_hook.* in production; tests
// substitute httptest values directly.
type restHookActivatorOptions struct {
	// AllowHTTP, when true, lets the activator POST to plain http://
	// endpoints. Production keeps this false (the URLValidator rejects
	// non-https endpoints at create time anyway); dev / e2e flips it on.
	AllowHTTP bool

	// Timeout bounds the entire handshake POST (dial + TLS + read).
	// A slow / hostile subscriber must not pin the API request.
	Timeout time.Duration

	// Logger receives one structured line per outcome. Nil falls back to
	// slog.Default().
	Logger *slog.Logger

	// httpClient is a test seam. Production constructs a fresh client
	// from Timeout; tests can substitute a transport that records the
	// outbound request.
	httpClient *http.Client
}

// restHookActivator implements handlers.ChannelActivator by POSTing a
// synthetic FHIR R5 handshake Bundle to the subscriber's endpoint and
// classifying the response. Replaces the no-op defaultActivator (D-2).
type restHookActivator struct {
	allowHTTP   bool
	timeout     time.Duration
	logger      *slog.Logger
	client      *http.Client
	successHits atomic.Uint64
	failureHits atomic.Uint64
}

// newRestHookActivator constructs the activator from opts. The returned
// activator is safe for concurrent use; it owns no per-subscription
// state.
func newRestHookActivator(opts restHookActivatorOptions) *restHookActivator {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := opts.httpClient
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 90 * time.Second,
				}).DialContext,
				MaxIdleConnsPerHost:   16,
				MaxConnsPerHost:       64,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ForceAttemptHTTP2:     true,
				TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12}, //nolint:gosec // 1.2 floor; subscribers vary.
			},
		}
	}
	return &restHookActivator{
		allowHTTP: opts.AllowHTTP,
		timeout:   timeout,
		logger:    logger,
		client:    client,
	}
}

// Close releases the activator's idle HTTP connections. The lifecycle
// module's PhaseCloseConnections hook calls this so the rest-hook
// activator does not pin TCP/TLS sockets past process shutdown
// (story #207). Idempotent — safe to call after an already-closed
// transport.
func (a *restHookActivator) Close() error {
	if a == nil || a.client == nil {
		return nil
	}
	if tr, ok := a.client.Transport.(*http.Transport); ok && tr != nil {
		tr.CloseIdleConnections()
	}
	return nil
}

// ActivateSubscription performs the FHIR R5 handshake against
// row.Endpoint. A 2xx response is HandshakeSucceeded; everything else
// (non-2xx, dial failure, timeout, scheme rejection) is HandshakeFailed.
// The error return is reserved for caller-side bugs; transport failures
// are classified into the outcome so the API audit trail records the
// real result instead of a synthetic "succeeded" (D-2).
func (a *restHookActivator) ActivateSubscription(ctx context.Context, row repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	endpoint := row.Endpoint
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Host == "" {
		a.logFail(row, "invalid endpoint url", 0)
		return handlers.HandshakeFailed, nil
	}
	if parsed.Scheme != "https" && !(a.allowHTTP && parsed.Scheme == "http") {
		a.logFail(row, "endpoint scheme not allowed", 0)
		return handlers.HandshakeFailed, nil
	}

	body, err := buildHandshakeBundle(row)
	if err != nil {
		// Caller-side bug — surface as error so the API logs it.
		return handlers.HandshakeFailed, fmt.Errorf("handshake: build bundle: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return handlers.HandshakeFailed, fmt.Errorf("handshake: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	req.Header.Set("X-Subscription-Id", row.ID.String())
	req.Header.Set("X-Subscription-Event-Number", "0")

	resp, err := a.client.Do(req)
	if err != nil {
		a.logFail(row, "transport error: "+err.Error(), 0)
		return handlers.HandshakeFailed, nil
	}
	defer resp.Body.Close()
	// Drain a small response body for clean keep-alive reuse without
	// consuming PHI into our memory.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		a.successHits.Add(1)
		a.logger.Info("rest-hook handshake succeeded",
			"subscription_id", row.ID,
			"endpoint", redactEndpointForLog(endpoint),
			"status", resp.StatusCode,
		)
		return handlers.HandshakeSucceeded, nil
	}
	a.logFail(row, "non-2xx response", resp.StatusCode)
	return handlers.HandshakeFailed, nil
}

func (a *restHookActivator) logFail(row repos.SubscriptionRow, reason string, status int) {
	a.failureHits.Add(1)
	a.logger.Warn("rest-hook handshake failed",
		"subscription_id", row.ID,
		"endpoint", redactEndpointForLog(row.Endpoint),
		"reason", reason,
		"status", status,
	)
}

// redactEndpointForLog drops the userinfo + query so a credentialed URL
// or query-token never enters the audit log (S-1.4-style redaction).
func redactEndpointForLog(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "<invalid>"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	return parsed.String()
}

// buildHandshakeBundle constructs a minimal FHIR R5 handshake Bundle:
// one entry, one SubscriptionStatus resource with type=handshake. The
// bytes are deterministic so a subscriber gateway that signs the body
// observes the same input across attempts.
func buildHandshakeBundle(row repos.SubscriptionRow) ([]byte, error) {
	type subscriptionStatus struct {
		ResourceType string `json:"resourceType"`
		Type         string `json:"type"`
		Status       string `json:"status"`
		Subscription struct {
			Reference string `json:"reference"`
		} `json:"subscription"`
		Topic string `json:"topic,omitempty"`
	}
	type entry struct {
		FullURL  string             `json:"fullUrl"`
		Resource subscriptionStatus `json:"resource"`
	}
	type bundle struct {
		ResourceType string  `json:"resourceType"`
		Type         string  `json:"type"`
		Timestamp    string  `json:"timestamp"`
		Entry        []entry `json:"entry"`
	}

	ss := subscriptionStatus{
		ResourceType: "SubscriptionStatus",
		Type:         "handshake",
		Status:       "requested",
		Topic:        row.TopicURL,
	}
	ss.Subscription.Reference = "Subscription/" + row.ID.String()
	b := bundle{
		ResourceType: "Bundle",
		Type:         "subscription-notification",
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Entry: []entry{
			{
				FullURL:  "urn:uuid:handshake",
				Resource: ss,
			},
		},
	}
	return json.Marshal(&b)
}

// emailActivator implements handlers.ChannelActivator by running a real
// SMTP RCPT-TO probe against the channel's configured relay. It
// replaces defaultActivator{} for the "email" channel type. OP #114
// (scope-reduced; WS halves moved to #114b/#114c).
//
// Activation classification:
//   - ProbeAccepted (RCPT TO 2xx) -> HandshakeSucceeded
//   - ProbeRejected (5xx, malformed mailto, STARTTLS-required-not-offered) -> HandshakeFailed
//   - ProbeTransient (4xx, dial error, deadline) -> HandshakeFailed
//
// HandshakeFailed surfaces upstream as the handlers.activate path
// flipping the row to repos.SubError, NOT keeping it stuck at
// "requested". Operators see a real failure reason in the audit log,
// not a synthetic "handshake succeeded" lie.
type emailActivator struct {
	channel *chemail.Channel
}

// newEmailActivator constructs an activator that delegates to
// channel.ProbeRecipient. The channel is the same instance the
// scheduler uses for delivery — activation reuses the configured relay
// host, port, STARTTLS policy, and AUTH mechanism so a probe that
// succeeds is a strong predictor that delivery against the same relay
// will at least connect and authenticate.
func newEmailActivator(ch *chemail.Channel) *emailActivator {
	return &emailActivator{channel: ch}
}

// unconfiguredEmailActivator is the fail-closed activator wired when
// the operator has not configured an SMTP relay
// (cfg.Channels.Email.SMTPHost empty). It returns HandshakeFailed with
// a reason the operator can see in the audit log — the lie of
// defaultActivator{} ("succeeded") is forbidden under epic #91 / OP
// #114. A subscription created against a binary with no SMTP relay
// cannot be activated; the row flips to error rather than to a
// silently-broken "active" state.
type unconfiguredEmailActivator struct{}

// ActivateSubscription always fails. The reason is folded into the
// audit log via handlers.activate's "handshake failed" path.
func (unconfiguredEmailActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return handlers.HandshakeFailed, fmt.Errorf("email channel not configured: SMTP relay missing (channels.email.smtp_host)")
}

// ActivateSubscription runs the RCPT-TO probe for sub.Endpoint. The
// caller (handlers.activate) treats HandshakeSucceeded as authorization
// to flip the row to "active" and HandshakeFailed as a terminal error.
// Errors are reserved for caller-side bugs; protocol/transport failures
// are folded into the HandshakeOutcome.
func (a *emailActivator) ActivateSubscription(ctx context.Context, sub repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if a == nil || a.channel == nil {
		return handlers.HandshakeFailed, fmt.Errorf("email activator: nil channel")
	}
	res, err := a.channel.ProbeRecipient(ctx, sub.Endpoint)
	if err != nil {
		return handlers.HandshakeFailed, err
	}
	if res.Outcome == chemail.ProbeAccepted {
		return handlers.HandshakeSucceeded, nil
	}
	return handlers.HandshakeFailed, nil
}
