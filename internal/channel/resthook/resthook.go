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
// allow request smuggling / forging.
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
}

// Reserved header-name prefixes that subscribers must never be able to
// forge (these are commonly trusted by reverse proxies).
var deniedPrefixes = []string{
	"x-forwarded-",
	"x-real-",
	"x-server-",
	"proxy-",
}

// Options configures a resthook Channel at construction time. Zero values
// fall back to package defaults.
type Options struct {
	// HTTPClient is the HTTP client used for outbound POST requests. If
	// nil, http.DefaultClient is used. Tests inject httptest.Server.Client().
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
	RequestTimeout time.Duration
}

// Channel implements the rest-hook delivery channel. Construct with New;
// Channel is safe for concurrent use.
type Channel struct {
	http      *http.Client
	metrics   channel.MetricsEmitter
	logger    *slog.Logger
	userAgent string
	timeout   time.Duration
}

// New constructs a rest-hook Channel.
func New(opts Options) (*Channel, error) {
	c := &Channel{
		http:      opts.HTTPClient,
		metrics:   opts.Metrics,
		logger:    opts.Logger,
		userAgent: opts.UserAgent,
		timeout:   opts.RequestTimeout,
	}
	if c.http == nil {
		// Default transport with a bounded DNS/connect timeout so a slow
		// system resolver cannot stall a delivery attempt past its deadline.
		// Per docs/low-level-design/channels.md §4.1 connection-pool defaults.
		tr := &http.Transport{
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
		}
		c.http = &http.Client{Transport: tr}
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
	if c.timeout <= 0 {
		c.timeout = DefaultRequestTimeout
	}
	return c, nil
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
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			StatusCode: status,
		}
	case status >= 500 && status < 600:
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     fmt.Sprintf("%dxx %d", status/100, status),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			StatusCode: status,
		}
	default:
		// Other 4xx — permanent.
		excerpt := readBodyExcerpt(resp.Body, DefaultMaxResponseBody)
		reason := fmt.Sprintf("%d %s", status, http.StatusText(status))
		if excerpt != "" {
			reason = fmt.Sprintf("%s: %s", reason, excerpt)
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

	// DNS errors: NXDOMAIN is permanent; SERVFAIL/TIMEOUT/temporary are transient.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return channel.PermanentFailure("dns nxdomain: " + dnsErr.Name)
		}
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
// or HTTP-date). Returns zero if the value is missing or unparseable —
// the scheduler treats zero as "no hint, use default backoff."
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

// formatTraceparent renders an envelope's correlation id as a W3C
// traceparent value. We use the correlation id as the trace-id when
// present, padding/truncating to 32 hex chars. If the correlation id is
// not a UUID, we still emit a syntactically-valid traceparent so
// subscribers receiving the header get a stable ID to log against.
func formatTraceparent(corrID string) string {
	traceID := strings.ToLower(strings.ReplaceAll(corrID, "-", ""))
	if len(traceID) > 32 {
		traceID = traceID[:32]
	}
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
