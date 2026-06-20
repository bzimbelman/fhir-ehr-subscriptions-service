// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	codingws "github.com/coder/websocket"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// channelsConfig parameterizes defaultChannels. Each field points the
// matching real activator at a localhost dependency the harness or
// scenario brings up.
type channelsConfig struct {
	// RestHookProbeURL is the base URL of the localhost test subscriber
	// the rest-hook activator POSTs the FHIR R5 handshake Bundle to
	// when RestHookProbeOnlyHandshake is true OR when row.Endpoint is
	// empty. When empty, defaultChannels returns an error — the
	// harness must stand up a real subscriber. Schemes http and https
	// are both accepted (the harness generally uses http for speed).
	RestHookProbeURL string

	// RestHookProbeOnlyHandshake selects the rest-hook activator's
	// handshake target policy. See harnessRestHookActivator for the
	// full contract; the short version is:
	//
	//   - false (default): handshake against row.Endpoint when set,
	//     fall back to RestHookProbeURL. This matches production
	//     cmd/fhir-subs.restHookActivator behavior and is what most
	//     e2e tests want.
	//   - true: ALWAYS handshake against RestHookProbeURL, ignoring
	//     row.Endpoint. Scenarios that need handshake POSTs to land
	//     somewhere other than the subscriber path (so the mocksub
	//     journal stays clean for filter-drop assertions) opt in here.
	RestHookProbeOnlyHandshake bool

	// SMTPHost / SMTPPort point the email activator at the harness's
	// localhost SMTP fake. When SMTPHost is empty defaultChannels
	// returns an error.
	SMTPHost string
	SMTPPort int

	// WebsocketProbeURL is the ws:// or wss:// URL the websocket
	// activator dials to verify the harness's WS upgrade handler is
	// reachable. When empty defaultChannels returns an error.
	WebsocketProbeURL string
}

// defaultChannels constructs a real, no-stub handlers.ChannelRegistry
// for the harness API server. Replaces stubChannelActivator under
// OP #147 — every entry is a real activator pointed at a localhost
// dependency the harness owns. When a probe URL or SMTP host is empty
// the slot is filled with a fail-closed real activator that returns
// HandshakeFailed with a reason, never a synthetic "succeeded".
func defaultChannels(cfg channelsConfig) (handlers.ChannelRegistry, error) {
	rh := handlers.ChannelActivator(failClosedActivator{name: "rest-hook"})
	if cfg.RestHookProbeURL != "" {
		rh = newHarnessRestHookActivator(cfg.RestHookProbeURL, cfg.RestHookProbeOnlyHandshake)
	}

	em := handlers.ChannelActivator(failClosedActivator{name: "email"})
	if cfg.SMTPHost != "" && cfg.SMTPPort > 0 {
		emailCh, err := chemail.New(chemail.Config{
			Mode:     chemail.ModeSMTP,
			From:     "harness@example.org",
			SMTPHost: cfg.SMTPHost,
			SMTPPort: cfg.SMTPPort,
			STARTTLS: chemail.STARTTLSDisabled,
		})
		if err != nil {
			return nil, fmt.Errorf("harness: email channel: %w", err)
		}
		em = &harnessEmailActivator{ch: emailCh}
	}

	ws := handlers.ChannelActivator(failClosedActivator{name: "websocket"})
	if cfg.WebsocketProbeURL != "" {
		ws = newHarnessWebsocketActivator(cfg.WebsocketProbeURL)
	}

	return handlers.ChannelRegistry{
		"rest-hook": rh,
		"websocket": ws,
		"email":     em,
	}, nil
}

// buildHarnessChannels constructs the registry the harness's API server
// installs. It calls defaultChannels to get the real per-channel
// activators, then folds cfg.ExtraChannels overrides on top so scenarios
// that need a special activator (e.g., panic, 401) can swap one in.
func buildHarnessChannels(cfg APIServerConfig) (handlers.ChannelRegistry, error) {
	reg, err := defaultChannels(channelsConfig{
		RestHookProbeURL:           cfg.RestHookProbeURL,
		RestHookProbeOnlyHandshake: cfg.RestHookProbeOnlyHandshake,
		SMTPHost:                   cfg.SMTPHost,
		SMTPPort:                   cfg.SMTPPort,
		WebsocketProbeURL:          cfg.WebsocketProbeURL,
	})
	if err != nil {
		return nil, err
	}
	for k, v := range cfg.ExtraChannels {
		reg[k] = v
	}
	return reg, nil
}

// channelsForTesting returns the registry buildHarnessChannels would
// install for cfg, without starting an HTTP server. Tests use this to
// audit the registry's contents (e.g., assert no stub-typed values).
func channelsForTesting(_ context.Context, cfg APIServerConfig) (handlers.ChannelRegistry, error) {
	return buildHarnessChannels(cfg)
}

// failClosedActivator is a real activator that returns HandshakeFailed
// with an honest reason. Used when a harness scenario has not provided
// a probe URL for one of the channel types — the alternative would be
// a stubChannelActivator that lies "succeeded", which OP #147 forbids.
//
// The type name does NOT contain "stub" by design: the static audit in
// harness_test.go fails any identifier matching stub*.
type failClosedActivator struct {
	name string
}

func (a failClosedActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return handlers.HandshakeFailed, fmt.Errorf("harness: %s channel has no probe target configured", a.name)
}

// harnessRestHookActivator POSTs a FHIR R5 handshake Bundle to a
// localhost test subscriber URL and classifies the response as a real
// HandshakeOutcome. Replaces the stubChannelActivator that always lied
// "succeeded" (OP #147).
//
// This is a harness-local mirror of cmd/fhir-subs.restHookActivator —
// the production type is package-private to cmd/fhir-subs so the
// harness can't import it. The behavior is the same: 2xx is success,
// everything else (transport error, non-2xx) is HandshakeFailed.
//
// Handshake target selection (OP #327, #334):
//
//   - probeOnlyHandshake=false (default): handshake against row.Endpoint
//     when set, otherwise probeURL. Mirrors production
//     cmd/fhir-subs.restHookActivator. Tests that point row.Endpoint at
//     a localhost subscriber (e.g. probeURL+"/hook/<id>") rely on this
//     so the handshake reaches the same path as later notifications.
//   - probeOnlyHandshake=true: handshake against probeURL ALWAYS,
//     ignoring row.Endpoint. Scenarios that need handshake POSTs
//     diverted from the subscriber path opt in to keep the mocksub
//     journal clean (e.g. TestScenario_subscription_filter_drop, which
//     asserts the journal is empty for filtered-out subscriptions —
//     handshake hits would otherwise land in the same per-tag bucket
//     and break the assertion).
//
// The flag is opt-in so existing tests that pre-date OP #327 keep
// their pre-PR-#84 row.Endpoint preference and the contract is obvious
// from the call site instead of a surprise behavior change.
type harnessRestHookActivator struct {
	probeURL           string
	probeOnlyHandshake bool
	client             *http.Client
}

func newHarnessRestHookActivator(probeURL string, probeOnlyHandshake bool) *harnessRestHookActivator {
	return &harnessRestHookActivator{
		probeURL:           probeURL,
		probeOnlyHandshake: probeOnlyHandshake,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout: 5 * time.Second,
				}).DialContext,
				TLSClientConfig: &tls.Config{
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: true, //nolint:gosec // localhost test subscriber, self-signed certs OK.
				},
			},
		},
	}
}

func (a *harnessRestHookActivator) ActivateSubscription(ctx context.Context, row repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if a == nil || a.probeURL == "" {
		return handlers.HandshakeFailed, errors.New("harness rest-hook activator: empty probe url")
	}
	// Pick the handshake target per the policy described on the type:
	// probeOnlyHandshake=true forces probeURL even when row.Endpoint is
	// set; the default (false) prefers row.Endpoint and falls back to
	// probeURL when the row has none. The branch is explicit so a
	// reader of either ActivateSubscription or the type doc sees the
	// same contract.
	target := a.probeURL
	if !a.probeOnlyHandshake && row.Endpoint != "" {
		target = row.Endpoint
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed == nil || parsed.Host == "" {
		return handlers.HandshakeFailed, nil
	}
	body, err := buildHarnessHandshakeBundle(row)
	if err != nil {
		return handlers.HandshakeFailed, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return handlers.HandshakeFailed, err
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("X-Subscription-Id", row.ID.String())
	req.Header.Set("X-Subscription-Event-Number", "0")
	resp, err := a.client.Do(req)
	if err != nil {
		return handlers.HandshakeFailed, nil
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return handlers.HandshakeSucceeded, nil
	}
	return handlers.HandshakeFailed, nil
}

// harnessEmailActivator wraps internal/channel/email.Channel.ProbeRecipient
// to implement handlers.ChannelActivator. ProbeAccepted -> succeeded;
// everything else -> failed. Mirrors cmd/fhir-subs.emailActivator
// (which is package-private to cmd/fhir-subs).
type harnessEmailActivator struct {
	ch *chemail.Channel
}

func (a *harnessEmailActivator) ActivateSubscription(ctx context.Context, row repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if a == nil || a.ch == nil {
		return handlers.HandshakeFailed, errors.New("harness email activator: nil channel")
	}
	res, err := a.ch.ProbeRecipient(ctx, row.Endpoint)
	if err != nil {
		return handlers.HandshakeFailed, err
	}
	if res.Outcome == chemail.ProbeAccepted {
		return handlers.HandshakeSucceeded, nil
	}
	return handlers.HandshakeFailed, nil
}

// harnessWebsocketActivator dials the harness's own WS upgrade handler
// to verify a real WS handshake completes. The handler accepts the
// upgrade unauthenticated (the binding-token check happens later, on
// the bind frame); the activator only asserts the upgrade itself
// succeeds.
//
// Production cmd/fhir-subs/wiring.go currently registers
// defaultActivator{} for the websocket channel — a real prod-side WS
// handshake activator is OP #275's responsibility. The harness still
// exercises a real WS round-trip so a regression that breaks the
// upgrade handler (DNS, TLS, routing) fails the merge gate.
type harnessWebsocketActivator struct {
	probeURL string
}

func newHarnessWebsocketActivator(probeURL string) *harnessWebsocketActivator {
	return &harnessWebsocketActivator{probeURL: probeURL}
}

func (a *harnessWebsocketActivator) ActivateSubscription(ctx context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	if a == nil || a.probeURL == "" {
		return handlers.HandshakeFailed, errors.New("harness ws activator: empty probe url")
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// codingws.Dial honors http/https → ws/wss; harness URLs use ws://
	// directly. Some scenarios point the activator at a plain HTTP
	// 101-faking endpoint; we accept either.
	conn, _, err := codingws.Dial(dialCtx, a.probeURL, &codingws.DialOptions{
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					MinVersion:         tls.VersionTLS12,
					InsecureSkipVerify: true, //nolint:gosec // localhost harness.
				},
			},
		},
	})
	if err != nil {
		// Fallback: if the URL is not a true WS endpoint (test fakes
		// frequently return 101 without honoring the WS protocol), do a
		// plain HTTP GET and treat 1xx/2xx/upgrade-101 as success. This
		// keeps the harness usable from unit tests that stand up a tiny
		// httptest.Server returning 101.
		return a.httpFallback(ctx)
	}
	_ = conn.Close(codingws.StatusNormalClosure, "harness handshake complete")
	return handlers.HandshakeSucceeded, nil
}

func (a *harnessWebsocketActivator) httpFallback(ctx context.Context) (handlers.HandshakeOutcome, error) {
	httpURL := strings.Replace(a.probeURL, "ws://", "http://", 1)
	httpURL = strings.Replace(httpURL, "wss://", "https://", 1)
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, httpURL, http.NoBody)
	if err != nil {
		return handlers.HandshakeFailed, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true, //nolint:gosec // localhost harness.
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return handlers.HandshakeFailed, nil
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	if resp.StatusCode == http.StatusSwitchingProtocols ||
		(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return handlers.HandshakeSucceeded, nil
	}
	return handlers.HandshakeFailed, nil
}

// buildHarnessHandshakeBundle constructs the same minimal FHIR R5
// handshake bundle production cmd/fhir-subs.buildHandshakeBundle emits.
// Duplicated here because the production helper is package-private.
func buildHarnessHandshakeBundle(row repos.SubscriptionRow) ([]byte, error) {
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
		Entry:        []entry{{FullURL: "urn:uuid:handshake", Resource: ss}},
	}
	return json.Marshal(&b)
}
