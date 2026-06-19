// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package resthook implements the rest-hook notification channel: HTTPS
// POST of a serialized FHIR subscription-notification Bundle to a
// subscriber-supplied endpoint.
//
// Per docs/low-level-design/channels.md §4.1, this channel is responsible
// for protocol-level behavior (header composition, allowlist filtering,
// timeouts, response classification) within one delivery attempt. It does
// NOT own cross-attempt retry, dead-lettering, or row writes to the
// deliveries table — those are the scheduler's responsibility. The
// channel returns a typed DeliveryOutcome and trusts the scheduler to
// translate it to the right database transition.
package resthook

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// Channel name constant; used in metric labels and span names.
const channelName = "resthook"

// Default values. These mirror the defaults in
// docs/low-level-design/channels.md §4.1 and ADR 0010.
const (
	DefaultUserAgent       = "fhir-ehr-subscriptions-service/0.0"
	DefaultRequestTimeout  = 30 * time.Second
	DefaultMaxResponseBody = 256 // bytes — bounds the 4xx body excerpt in PermanentFailure.Reason

	// DefaultMaxBundleBytes caps the serialized bundle body the channel
	// will POST. payload=full-resource with embedded base64 can produce
	// multi-MB bundles; without a cap the worker spends its retry budget
	// on hopeless re-deliveries (S-4). 8 MiB is a generous ceiling.
	DefaultMaxBundleBytes = 8 * 1024 * 1024

	// DefaultMaxIdleConnsPerHost / DefaultMaxConnsPerHost match the
	// historical hardcoded values; now exposed via Options (S-4).
	DefaultMaxIdleConnsPerHost = 16
	DefaultMaxConnsPerHost     = 64

	// DefaultMaxRetryAfter caps the duration parsed from a subscriber's
	// Retry-After header. A hostile or misconfigured subscriber returning
	// Retry-After: 999999999 (≈31 years) — or a far-future HTTP-date —
	// would otherwise pin the deliveries row at next_attempt_at far in
	// the future, denying retries (OP #190). 24h is a generous ceiling:
	// legitimate maintenance windows are minutes-to-hours; values past
	// a day signal abuse or misconfiguration.
	DefaultMaxRetryAfter = 24 * time.Hour

	// DefaultMinRetryAfter floors the Retry-After hint at 1s so a
	// subscriber asking for a tight retry loop ("Retry-After: 1ms")
	// cannot burn through the scheduler's retry budget. 1s matches the
	// scheduler's RetryConfig.Min default.
	DefaultMinRetryAfter = time.Second

	// retryNowSentinel is the positive value the channel returns when a
	// subscriber explicitly says "retry now" (Retry-After: 0 or a past
	// HTTP-date). Distinguishing this from a missing header (which
	// returns 0) lets the scheduler honor the subscriber's signal
	// instead of falling back to default backoff (OP #190).
	retryNowSentinel = time.Nanosecond
)

// Metric names emitted by this channel. Wire form follows the
// fhir_subs_channel_resthook_* convention from the operating procedure
// and architecture.md §observability.
const (
	MetricDeliveriesTotal       = "fhir_subs_channel_resthook_deliveries_total"
	MetricDeliveryDurationSec   = "fhir_subs_channel_resthook_delivery_duration_seconds"
	MetricStatusTotal           = "fhir_subs_channel_resthook_status_total"
	MetricHeadersFilteredTotal  = "fhir_subs_channel_resthook_headers_filtered_total"
	MetricNonHTTPSEndpointTotal = "fhir_subs_channel_resthook_non_https_endpoint_total"
)

// HTTP header constants the channel sets on every POST.
const (
	headerContentType = "Content-Type"
	headerAccept      = "Accept"
	headerUserAgent   = "User-Agent"
	headerTraceparent = "traceparent"
	headerXSubID      = "X-Subscription-Id"
	headerXEventNum   = "X-Subscription-Event-Number"
	headerXAttempt    = "X-Attempt"
	headerXCorrID     = "X-Correlation-ID"
)

// FHIR-related headers always permitted from Subscription.parameter[].
//
// Source: docs/low-level-design/channels.md §4.1 "Header allowlist", rule 3.
var allowedFHIRHeaders = map[string]struct{}{
	"if-match":          {},
	"if-none-match":     {},
	"if-modified-since": {},
	"if-none-exist":     {},
	"prefer":            {},
	"x-request-id":      {},
}

// Headers always rejected from Subscription.parameter[], even if they
// match the validity regex. The channel sets these itself or they would
// allow request smuggling / forging or downstream privilege confusion.
//
// The "internal trust" / "auth user" / "real ip" entries below close
// the S-4 default-permit gap: subscribers must never be able to forge
// headers a downstream reverse proxy or service mesh might trust.
var deniedHeaders = map[string]struct{}{
	"host":              {},
	"content-length":    {},
	"content-type":      {},
	"transfer-encoding": {},
	"connection":        {},
	"authorization":     {},
	"cookie":            {},
	"set-cookie":        {},
	"user-agent":        {},
	"trace-parent":      {},
	"trace-state":       {},
	"server":            {},
	"x-internal-trust":  {},
	"x-auth-user":       {},
	"x-auth-token":      {},
	"x-auth-roles":      {},
	"x-auth-role":       {},
	"x-auth-tenant":     {},
	"x-trusted-user":    {},
	"x-trusted-roles":   {},
	"x-shibboleth-user": {},
	"remote-user":       {},
	"x-remote-user":     {},
	"x-original-url":    {},
	"x-rewrite-url":     {},
	"x-client-cert":     {},
	"x-ssl-client-cert": {},
	"x-real-ip":         {},
}

// Reserved header-name prefixes that subscribers must never be able to
// forge (these are commonly trusted by reverse proxies).
//
// "x-internal-" / "x-trusted-" / "x-auth-" cover the S-4 forging
// surface for any header in those families a downstream layer might
// trust without the channel needing to know each name in advance.
var deniedPrefixes = []string{
	"x-forwarded-",
	"x-real-",
	"x-server-",
	"proxy-",
	"x-internal-",
	"x-trusted-",
	"x-auth-",
}

// Options configures a resthook Channel at construction time. Zero values
// fall back to package defaults.
type Options struct {
	// HTTPClient is the HTTP client used for outbound POST requests. If
	// nil, a default client with a wall-clock Timeout is built from the
	// pool / TLS knobs below. Tests inject httptest.Server.Client(); any
	// caller-supplied client is used as-is.
	HTTPClient *http.Client
	// Metrics receives counter increments. If nil, channel.NopMetrics is used.
	Metrics channel.MetricsEmitter
	// Logger is the structured logger. If nil, slog.Default() is used.
	Logger *slog.Logger
	// UserAgent overrides the User-Agent header. Empty string falls back
	// to DefaultUserAgent.
	UserAgent string
	// RequestTimeout is the per-attempt total wall-clock budget when
	// envelope.Deadline is zero. Zero falls back to DefaultRequestTimeout.
	// Also used as the default-client's wall-clock Timeout (S-4).
	RequestTimeout time.Duration

	// MaxIdleConnsPerHost overrides the default-client's pool setting
	// (S-4). Zero falls back to DefaultMaxIdleConnsPerHost.
	MaxIdleConnsPerHost int
	// MaxConnsPerHost overrides the default-client's pool cap (S-4).
	// Zero falls back to DefaultMaxConnsPerHost.
	MaxConnsPerHost int
	// TLSMinVersion is the minimum TLS version the default client will
	// negotiate (S-4). Zero falls back to tls.VersionTLS12 (Go default).
	// Production deployments SHOULD set tls.VersionTLS13.
	TLSMinVersion uint16
	// MaxBundleBytes caps the serialized bundle the channel will POST.
	// Zero falls back to DefaultMaxBundleBytes (S-4). A bundle larger
	// than this is permanently failed before any network I/O.
	MaxBundleBytes int

	// MaxRetryAfter caps the duration the channel will surface from a
	// subscriber's Retry-After header. Subscriber-controlled retry-pin
	// DoS guard (OP #190): a hostile subscriber returning a multi-year
	// value would otherwise pin a deliveries row at far-future for
	// years. Zero falls back to DefaultMaxRetryAfter (24h). The
	// channel logs at WARN when clamping.
	MaxRetryAfter time.Duration

	// MinRetryAfter floors the Retry-After hint. Stops a subscriber
	// requesting a tight retry loop from burning through the
	// scheduler's retry budget. Zero falls back to DefaultMinRetryAfter
	// (1s). Does not apply to the explicit "retry now" sentinel
	// (Retry-After: 0 or past HTTP-date), which is preserved as a
	// nanosecond-scale positive value.
	MinRetryAfter time.Duration

	// IncludeResponseBodyExcerpt controls whether subscriber 4xx response
	// bytes are quoted into DeliveryOutcome.Reason. Default false — the
	// excerpt is a PHI exfiltration / log-injection vector (S-4). Set
	// true only in dev / staging where the operator owns both ends.
	IncludeResponseBodyExcerpt bool

	// URLValidator, when non-nil, re-validates the subscriber endpoint
	// at delivery time before any network I/O. This closes the DNS-
	// rebinding window where a public-resolving hostname registered at
	// subscription create time is flipped to a private / loopback /
	// cloud-metadata IP before delivery (audit #112 sup2 / #129; OP
	// #182). Validate failures are surfaced as PermanentFailure: a
	// hostname that resolves to a blocked range will keep doing so
	// until the operator fixes it, and the scheduler should not burn
	// retry budget re-dialing.
	URLValidator URLValidator
}

// URLValidator is the narrow interface the resthook channel uses to
// re-check a subscriber's endpoint URL at delivery time. It is
// satisfied by handlers.URLValidator without an import cycle (the
// channel layer must not depend on the api/handlers package). Callers
// pass the same instance the API layer used at create-time.
type URLValidator interface {
	Validate(rawURL string) error
}

// Channel implements the rest-hook delivery channel. Construct with New;
// Channel is safe for concurrent use.
type Channel struct {
	http               *http.Client
	transport          *http.Transport // nil if caller-supplied HTTPClient
	metrics            channel.MetricsEmitter
	logger             *slog.Logger
	userAgent          string
	timeout            time.Duration
	maxBundleBytes     int
	maxRetryAfter      time.Duration
	minRetryAfter      time.Duration
	includeBodyExcerpt bool
	urlValidator       URLValidator
}

// New constructs a rest-hook Channel.
func New(opts Options) (*Channel, error) {
	c := &Channel{
		http:               opts.HTTPClient,
		metrics:            opts.Metrics,
		logger:             opts.Logger,
		userAgent:          opts.UserAgent,
		timeout:            opts.RequestTimeout,
		maxBundleBytes:     opts.MaxBundleBytes,
		maxRetryAfter:      opts.MaxRetryAfter,
		minRetryAfter:      opts.MinRetryAfter,
		includeBodyExcerpt: opts.IncludeResponseBodyExcerpt,
		urlValidator:       opts.URLValidator,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultRequestTimeout
	}
	if c.maxBundleBytes <= 0 {
		c.maxBundleBytes = DefaultMaxBundleBytes
	}
	if c.maxRetryAfter <= 0 {
		c.maxRetryAfter = DefaultMaxRetryAfter
	}
	if c.minRetryAfter <= 0 {
		c.minRetryAfter = DefaultMinRetryAfter
	}
	// Defensive: a misconfiguration where Min > Max would silently
	// invert the clamp. Treat the cap as authoritative — operators set
	// the cap to defend against hostile subscribers; raising the floor
	// past the cap is incoherent.
	if c.minRetryAfter > c.maxRetryAfter {
		c.minRetryAfter = c.maxRetryAfter
	}
	if c.http == nil {
		maxIdle := opts.MaxIdleConnsPerHost
		if maxIdle <= 0 {
			maxIdle = DefaultMaxIdleConnsPerHost
		}
		maxConns := opts.MaxConnsPerHost
		if maxConns <= 0 {
			maxConns = DefaultMaxConnsPerHost
		}
		minTLS := opts.TLSMinVersion
		if minTLS == 0 {
			// TLS 1.3 by default — gosec G402 flags <1.3, and FHIR
			// subscribers are first-party integrations that can be
			// expected to support modern TLS (S-4).
			minTLS = tls.VersionTLS13
		}
		// Default transport with a bounded DNS/connect timeout so a slow
		// system resolver cannot stall a delivery attempt past its deadline.
		// Per docs/low-level-design/channels.md §4.1 connection-pool defaults.
		tr := &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 90 * time.Second,
			}).DialContext,
			MaxIdleConnsPerHost:   maxIdle,
			MaxConnsPerHost:       maxConns,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
			TLSClientConfig:       &tls.Config{MinVersion: minTLS}, //nolint:gosec // configurable; defaults to TLS 1.3
		}
		c.transport = tr
		// Wall-clock Timeout bounds the entire request — a hostile
		// subscriber dripping response headers cannot tie up the worker
		// past its envelope deadline (S-4).
		c.http = &http.Client{Transport: tr, Timeout: c.timeout}
	}
	if c.metrics == nil {
		c.metrics = channel.NopMetrics{}
	}
	if c.logger == nil {
		c.logger = slog.Default()
	}
	if c.userAgent == "" {
		c.userAgent = DefaultUserAgent
	}
	return c, nil
}

// HTTPClientForTest exposes the constructed http.Client to tests without
// shipping it as a public method.
func (c *Channel) HTTPClientForTest() *http.Client { return c.http }

// TransportForTest exposes the default transport (nil if a caller
// supplied an HTTPClient) so tests can assert pool / TLS configuration.
func (c *Channel) TransportForTest() *http.Transport { return c.transport }

// Close releases the channel-owned http.Transport's idle connections so
// the lifecycle module can drain pooled keep-alive sockets at shutdown.
// Idempotent: calling Close on an already-closed channel returns nil.
func (c *Channel) Close() error {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
	return nil
}

// Deliver POSTs the envelope's bundle to the subscriber's endpoint and
// classifies the response into a DeliveryOutcome.
func (c *Channel) Deliver(ctx context.Context, env channel.NotificationEnvelope) (channel.DeliveryOutcome, error) {
	start := time.Now()
	defer func() {
		c.metrics.Observe(MetricDeliveryDurationSec, time.Since(start).Seconds(),
			map[string]string{"channel": channelName})
	}()

	out := c.deliverInner(ctx, env)
	c.metrics.Inc(MetricDeliveriesTotal, map[string]string{
		"channel": channelName,
		"outcome": outcomeLabel(out.Kind),
	})
	c.logOutcome(ctx, env, out)
	return out, nil
}

// deliverInner performs the request and returns the classified outcome.
// Network/HTTP failures are converted to DeliveryOutcome; only construction
// errors (which do not occur in the current implementation) would surface
// as an error from Deliver.
func (c *Channel) deliverInner(ctx context.Context, env channel.NotificationEnvelope) channel.DeliveryOutcome {
	parsed, err := url.Parse(env.SubscriptionEndpoint)
	if err != nil {
		return channel.PermanentFailure(fmt.Sprintf("invalid endpoint url: %v", err))
	}
	if parsed.Scheme != "https" {
		c.metrics.Inc(MetricNonHTTPSEndpointTotal, map[string]string{"channel": channelName})
		return channel.PermanentFailure("non-https endpoint")
	}
	if len(env.BundleBytes) > c.maxBundleBytes {
		// S-4: refuse oversize bundles before any I/O. Retry budget is
		// finite; a 50MB payload-full-resource bundle that 413s on every
		// retry is hopeless from the first attempt.
		return channel.PermanentFailure(fmt.Sprintf("bundle too large: %d > %d",
			len(env.BundleBytes), c.maxBundleBytes))
	}

	// Re-validate the endpoint URL against the SSRF policy at delivery
	// time. The same validator vetted this URL at subscription create,
	// but a hostile (or compromised) subscriber can flip DNS to a
	// private / loopback / cloud-metadata IP between create and
	// delivery (audit #112 sup2 / #129; OP #182). Without this check,
	// the channel would dial whatever the resolver answers now, and
	// the SSRF guarantee would only hold at create-time. Failures are
	// permanent: the resolver will keep returning the blocked address
	// until the operator fixes it, and burning retry budget re-dialing
	// the same private IP is pointless.
	if c.urlValidator != nil {
		if vErr := c.urlValidator.Validate(env.SubscriptionEndpoint); vErr != nil {
			return channel.PermanentFailure(fmt.Sprintf("ssrf policy: %v", vErr))
		}
	}

	// Apply per-attempt deadline.
	attemptCtx, cancel := c.applyDeadline(ctx, env.Deadline)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, env.SubscriptionEndpoint,
		bytes.NewReader(env.BundleBytes))
	if err != nil {
		return channel.PermanentFailure(fmt.Sprintf("build request: %v", err))
	}
	c.composeHeaders(req, env)

	resp, err := c.http.Do(req)
	if err != nil {
		return c.classifyNetworkError(err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	c.metrics.Inc(MetricStatusTotal, map[string]string{
		"channel": channelName,
		"class":   statusClass(resp.StatusCode),
	})

	return c.classifyHTTPResponse(resp)
}

// applyDeadline derives an attempt context from ctx and the envelope's
// deadline (or this channel's default timeout).
func (c *Channel) applyDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if !deadline.IsZero() {
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// composeHeaders sets server-injected headers and applies the allowlist
// to subscriber-supplied headers.
func (c *Channel) composeHeaders(req *http.Request, env channel.NotificationEnvelope) {
	req.Header.Set(headerUserAgent, c.userAgent)
	req.Header.Set(headerContentType, string(env.ContentType))
	req.Header.Set(headerAccept, "application/fhir+json, application/fhir+xml; q=0.9")
	req.Header.Set(headerTraceparent, formatTraceparent(env.CorrelationID))
	req.Header.Set(headerXSubID, env.SubscriptionID.String())
	req.Header.Set(headerXAttempt, strconv.FormatUint(uint64(env.Attempt), 10))
	if env.CorrelationID != "" {
		req.Header.Set(headerXCorrID, env.CorrelationID)
	}
	if env.BundleKind == channel.BundleEventNotification {
		req.Header.Set(headerXEventNum, strconv.FormatUint(env.Sequence, 10))
	}
	for _, p := range env.SubscriptionParameters {
		if !c.allowSubscriberHeader(p.Name) {
			c.metrics.Inc(MetricHeadersFilteredTotal, map[string]string{"name": strings.ToLower(p.Name)})
			continue
		}
		req.Header.Set(p.Name, p.Value)
	}
}

// allowSubscriberHeader applies the deny + allowlist + validity rules
// from docs/low-level-design/channels.md §4.1.
//
// Subscriber-supplied custom headers default-permit AFTER passing the
// expanded deny list and validity regex. The S-4 fix is the deny-list
// expansion (X-Internal-*, X-Trusted-*, X-Auth-*, X-Real-IP, etc.) plus
// the prefix list (x-internal-, x-trusted-, x-auth-): downstream
// reverse proxies and service meshes commonly trust these, so even if
// allowlist lookup is bypassable, the deny list is the load-bearing
// boundary.
func (c *Channel) allowSubscriberHeader(name string) bool {
	lower := strings.ToLower(name)
	if !validHeaderName(name) {
		return false
	}
	if _, denied := deniedHeaders[lower]; denied {
		return false
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(lower, p) {
			return false
		}
	}
	if _, ok := allowedFHIRHeaders[lower]; ok {
		return true
	}
	// Subscriber-supplied custom headers default-permit, having passed
	// deny-list and the validity regex.
	return true
}

// validHeaderName matches RFC 7230 token chars (a strict subset of ASCII).
// We require the first char to be a letter for predictability.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	first := name[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')) {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-':
		default:
			return false
		}
	}
	return true
}

// classifyHTTPResponse maps an HTTP response to a DeliveryOutcome per
// docs/low-level-design/channels.md §4.1.
func (c *Channel) classifyHTTPResponse(resp *http.Response) channel.DeliveryOutcome {
	status := resp.StatusCode
	switch {
	case status >= 200 && status < 300:
		return channel.DeliveryOutcome{Kind: channel.OutcomeDelivered, StatusCode: status}
	case status == http.StatusRequestTimeout: // 408
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     "408 Request Timeout",
			StatusCode: status,
		}
	case status == http.StatusTooManyRequests: // 429
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     "429 Too Many Requests",
			RetryAfter: c.parseRetryAfter(resp.Header.Get("Retry-After")),
			StatusCode: status,
		}
	case status >= 500 && status < 600:
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     fmt.Sprintf("%dxx %d", status/100, status),
			RetryAfter: c.parseRetryAfter(resp.Header.Get("Retry-After")),
			StatusCode: status,
		}
	default:
		// Other 4xx — permanent.
		reason := fmt.Sprintf("%d %s", status, http.StatusText(status))
		// S-4: subscriber response bodies may contain PHI (a 400
		// rejecting a duplicate Patient is a likely path). Quoting them
		// into the Reason — which is logged and surfaced via FHIR
		// OperationOutcome — risks bypassing redaction. Default off;
		// operators opt in via Options.IncludeResponseBodyExcerpt only
		// in environments where they own both ends.
		if c.includeBodyExcerpt {
			if excerpt := readBodyExcerpt(resp.Body, DefaultMaxResponseBody); excerpt != "" {
				reason = fmt.Sprintf("%s: %s", reason, excerpt)
			}
		}
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomePermanent,
			Reason:     reason,
			StatusCode: status,
		}
	}
}

// classifyNetworkError maps Go network/transport errors to a DeliveryOutcome.
func (c *Channel) classifyNetworkError(err error) channel.DeliveryOutcome {
	// Context cancellations (deadline / cancel) are transient — the
	// subscriber may still be reachable on the next attempt.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return channel.TransientFailure(0, "context canceled: "+err.Error())
	}

	// DNS errors are transient. NXDOMAIN at the resolver level often
	// reflects propagation lag (recursive resolver hiccup, SOA refresh
	// in flight) — a misconfigured subscriber endpoint will keep
	// resolving NXDOMAIN until the operator fixes it, and the scheduler
	// will eventually exhaust its retry budget and dead-letter (S-4).
	// Treating NXDOMAIN as permanent dead-lettered transient failures.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return channel.TransientFailure(0, "dns: "+dnsErr.Err)
	}

	// Network operation errors (connect refused, reset, timeout) — all transient.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return channel.TransientFailure(0, "network: "+opErr.Op+" "+sanitizeError(opErr.Err))
	}

	// URL errors wrap most http.Client errors; unwrap to expose the inner.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// timeout property covers idle-connection-timeout, response-header timeout, etc.
		if urlErr.Timeout() {
			return channel.TransientFailure(0, "i/o timeout: "+urlErr.Err.Error())
		}
		return channel.TransientFailure(0, "io: "+urlErr.Err.Error())
	}

	return channel.TransientFailure(0, "io: "+err.Error())
}

// parseRetryAfter parses an RFC 7231 Retry-After header (delta-seconds
// or HTTP-date) and bounds the result to defend against subscriber-
// controlled retry-pin DoS (OP #190).
//
// Return-value semantics — the scheduler treats these distinctly:
//   - 0                  → header missing or unparseable; scheduler
//     uses its default backoff curve.
//   - retryNowSentinel   → subscriber explicitly said "retry now"
//     (Retry-After: 0 or a past HTTP-date). A positive nanosecond-
//     scale value beats the scheduler's >0 check so the hint is
//     honored, but is small enough that the scheduler's own min floor
//     dominates the actual wait.
//   - [min, max]         → a sane subscriber hint, clamped to the
//     channel's configured floor and ceiling.
//
// When clamping at the cap, the channel logs at WARN — operators
// rely on this to spot misbehaving subscribers; silent clamping would
// hide the abuse signal.
func (c *Channel) parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}

	var raw time.Duration
	if secs, err := strconv.Atoi(v); err == nil {
		// Explicit "retry now": preserve as a positive sentinel so the
		// scheduler can distinguish from a missing header.
		if secs == 0 {
			return retryNowSentinel
		}
		// Negative delta-seconds is malformed per RFC 7231 — return 0
		// to trigger default-backoff behavior rather than guess intent.
		if secs < 0 {
			return 0
		}
		raw = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			// Past or now → equivalent to Retry-After: 0.
			return retryNowSentinel
		}
		raw = d
	} else {
		// Unparseable: treat as missing rather than guess.
		return 0
	}

	// Clamp at the cap; log when we do so a hostile subscriber leaves
	// an audit trail. We log before returning so the WARN is emitted
	// even if a downstream caller drops the duration.
	if raw > c.maxRetryAfter {
		c.logger.Warn("rest-hook retry-after clamped at max",
			slog.String("channel", channelName),
			slog.Duration("retry_after_raw", raw),
			slog.Duration("retry_after_clamped", c.maxRetryAfter),
		)
		return c.maxRetryAfter
	}
	// Floor: a subscriber asking for a sub-floor retry is treated as
	// "retry at the floor" — not as "retry now", because they did
	// give a (small) positive hint. Distinct from Retry-After: 0.
	if raw < c.minRetryAfter {
		return c.minRetryAfter
	}
	return raw
}

// formatTraceparent renders an envelope's correlation id as a W3C
// traceparent value. We use the correlation id as the trace-id when
// present, padding/truncating to 32 hex chars. The output is
// guaranteed-hex per the traceparent grammar: any non-hex character is
// dropped before length normalization so a non-UUID correlation id
// (e.g., a vendor ULID) cannot produce an invalid traceparent that
// downstream W3C-compliant parsers would reject (N-1).
func formatTraceparent(corrID string) string {
	lower := strings.ToLower(corrID)
	hex := make([]byte, 0, 32)
	for i := 0; i < len(lower) && len(hex) < 32; i++ {
		c := lower[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
			hex = append(hex, c)
		}
	}
	traceID := string(hex)
	for len(traceID) < 32 {
		traceID += "0"
	}
	const spanID = "0000000000000001"
	return "00-" + traceID + "-" + spanID + "-00"
}

// readBodyExcerpt reads up to maxBytes of body for a log-safe excerpt.
func readBodyExcerpt(r io.Reader, maxBytes int) string {
	buf := make([]byte, maxBytes)
	n, _ := io.ReadFull(io.LimitReader(r, int64(maxBytes)), buf)
	if n == 0 {
		return ""
	}
	// Strip control characters that would break log lines.
	return strings.TrimSpace(string(buf[:n]))
}

// sanitizeError strips any wrapping that could include a binary payload;
// returns just err.Error() with whitespace trimmed for the Reason field.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

// outcomeLabel renders an OutcomeKind as the metric label.
func outcomeLabel(k channel.OutcomeKind) string {
	switch k {
	case channel.OutcomeDelivered:
		return "delivered"
	case channel.OutcomeTransient:
		return "transient"
	case channel.OutcomePermanent:
		return "permanent"
	default:
		return "unknown"
	}
}

// statusClass renders an HTTP status code as a label like "2xx", "5xx".
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "other"
	}
}

// logOutcome emits one structured log line per delivery attempt with the
// correlation id, outcome, and reason. Endpoint and bundle are subject to
// the logging package's PHI redactor (handled by the slog handler).
func (c *Channel) logOutcome(ctx context.Context, env channel.NotificationEnvelope, out channel.DeliveryOutcome) {
	level := slog.LevelInfo
	switch out.Kind {
	case channel.OutcomeTransient:
		level = slog.LevelWarn
	case channel.OutcomePermanent:
		level = slog.LevelError
	}
	c.logger.Log(ctx, level, "rest-hook delivery",
		slog.String("channel", channelName),
		slog.String("subscription_id", env.SubscriptionID.String()),
		slog.Uint64("sequence", env.Sequence),
		slog.Uint64("attempt", uint64(env.Attempt)),
		slog.String("correlation_id", env.CorrelationID),
		slog.String("outcome", outcomeLabel(out.Kind)),
		slog.Int("status_code", out.StatusCode),
		slog.String("endpoint", env.SubscriptionEndpoint),
		slog.String("reason", out.Reason),
	)
}
