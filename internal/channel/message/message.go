// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package message implements the FHIR messaging notification channel:
// HTTPS POST of a FHIR Bundle of type "message" that wraps the
// subscription-notification Bundle in a MessageHeader resource.
//
// Per docs/low-level-design/channels.md §4.4, this channel is the only
// built-in channel that re-serializes the Bundle on the way out — the
// FHIR messaging interaction requires the outer wrap. The HTTP transport
// reuses rest-hook's request building, header policy, and response
// classification (channels share an HTTP classifier per LLD §4.4 and §9).
//
// Like every Channel SPI implementation, this channel owns protocol-level
// behavior within one delivery attempt. It does NOT own cross-attempt
// retry, dead-lettering, or row writes to the deliveries table — those
// belong to the scheduler. The channel returns a typed DeliveryOutcome
// and trusts the scheduler to translate it to the right database
// transition.
package message

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
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

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/correlation"
)

// channelName is used in metric labels and span names.
const channelName = "message"

// EventCodingSystem is the spec-aligned code system per ADR 0008 #13.
const EventCodingSystem = "http://terminology.hl7.org/CodeSystem/subscription-notification-type"

// Default values mirror rest-hook (LLD §4.4: "delegates to channels.rest_hook
// defaults where unset").
const (
	DefaultUserAgent       = "fhir-ehr-subscriptions-service/0.0"
	DefaultRequestTimeout  = 30 * time.Second
	DefaultMaxResponseBody = 256

	// DefaultMaxIdleConnsPerHost / DefaultMaxConnsPerHost match rest-hook
	// (S-5; exposed via Options).
	DefaultMaxIdleConnsPerHost = 16
	DefaultMaxConnsPerHost     = 64
)

// Metric names emitted by this channel. Per LLD §9.
const (
	MetricDeliveriesTotal       = "fhir_subs_channel_message_deliveries_total"
	MetricDeliveryDurationSec   = "fhir_subs_channel_message_delivery_duration_seconds"
	MetricStatusTotal           = "fhir_subs_channel_message_status_total"
	MetricHeadersFilteredTotal  = "fhir_subs_channel_message_headers_filtered_total"
	MetricNonHTTPSEndpointTotal = "fhir_subs_channel_message_non_https_endpoint_total"
	MetricWrappingDurationSec   = "fhir_subs_channel_message_wrapping_duration_seconds"
	MetricWrapFailureTotal      = "fhir_subs_channel_message_wrap_failures_total"
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

// FHIR-related headers always permitted from Subscription.parameter[]
// per LLD §4.1 rule 3.
var allowedFHIRHeaders = map[string]struct{}{
	"if-match":          {},
	"if-none-match":     {},
	"if-modified-since": {},
	"if-none-exist":     {},
	"prefer":            {},
	"x-request-id":      {},
}

// Headers always rejected — the channel sets these or they would allow
// request smuggling / forging or downstream privilege confusion
// (LLD §4.1 rule 1; S-5 expansion to close default-permit forging gap).
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

// Reserved-prefix deny — proxies trust these (LLD §4.1 rule 2; S-5
// expansion).
var deniedPrefixes = []string{
	"x-forwarded-",
	"x-real-",
	"x-server-",
	"proxy-",
	"x-internal-",
	"x-trusted-",
	"x-auth-",
}

// Options configures a message Channel at construction time. Zero values
// fall back to package defaults.
type Options struct {
	// HTTPClient is the HTTP client used for outbound POST requests. If
	// nil, a default client with a wall-clock Timeout is built from the
	// pool / TLS knobs below.
	HTTPClient *http.Client
	// Metrics receives counter and histogram samples. If nil, channel.NopMetrics.
	Metrics channel.MetricsEmitter
	// Logger is the structured logger. If nil, slog.Default().
	Logger *slog.Logger
	// UserAgent overrides the User-Agent header. Empty -> DefaultUserAgent.
	UserAgent string
	// RequestTimeout is the per-attempt total wall-clock budget when
	// envelope.Deadline is zero. Zero -> DefaultRequestTimeout. Also
	// used as the default-client's wall-clock Timeout (S-5).
	RequestTimeout time.Duration
	// ServerEndpoint is the URI placed in MessageHeader.source.endpoint.
	// Per LLD §4.4 this is "the server's identity URI
	// (config.deployment.facility_id lifted into a URI)". Empty omits
	// MessageHeader.source.endpoint.
	ServerEndpoint string

	// MaxIdleConnsPerHost overrides the default-client's pool setting
	// (S-5). Zero falls back to DefaultMaxIdleConnsPerHost.
	MaxIdleConnsPerHost int
	// MaxConnsPerHost overrides the default-client's pool cap (S-5).
	// Zero falls back to DefaultMaxConnsPerHost.
	MaxConnsPerHost int
	// TLSMinVersion is the minimum TLS version the default client will
	// negotiate (S-5). Zero falls back to tls.VersionTLS12.
	TLSMinVersion uint16
}

// Channel implements the FHIR messaging delivery channel. Construct with
// New; safe for concurrent use.
type Channel struct {
	http           *http.Client
	transport      *http.Transport // nil if caller-supplied HTTPClient
	metrics        channel.MetricsEmitter
	logger         *slog.Logger
	userAgent      string
	timeout        time.Duration
	serverEndpoint string
}

// New constructs a message Channel.
func New(opts Options) (*Channel, error) {
	c := &Channel{
		http:           opts.HTTPClient,
		metrics:        opts.Metrics,
		logger:         opts.Logger,
		userAgent:      opts.UserAgent,
		timeout:        opts.RequestTimeout,
		serverEndpoint: opts.ServerEndpoint,
	}
	if c.timeout <= 0 {
		c.timeout = DefaultRequestTimeout
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
			// expected to support modern TLS (S-5).
			minTLS = tls.VersionTLS13
		}
		// Default transport with a bounded DNS/connect timeout. Mirrors
		// resthook's defaults — same connection-pool envelope (LLD §4.1).
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
		// Wall-clock Timeout bounds the entire request — header-drip
		// from a hostile subscriber cannot tie up the worker past its
		// envelope deadline (S-5).
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

// HTTPClientForTest exposes the constructed http.Client to tests.
func (c *Channel) HTTPClientForTest() *http.Client { return c.http }

// TransportForTest exposes the default transport (nil if caller supplied
// an HTTPClient).
func (c *Channel) TransportForTest() *http.Transport { return c.transport }

// ValidateContentType reports whether ct is acceptable on a Subscription
// targeting this channel. The API layer SHOULD call this at create time
// so non-fhir+json subscriptions fail-closed at the boundary rather than
// running through the scheduler retry budget before being permanently
// failed at first delivery (S-5).
func (c *Channel) ValidateContentType(ct channel.ContentType) error {
	if ct != channel.ContentTypeFHIRJSON {
		return fmt.Errorf("content type %q not supported by message channel in v1 (json only)", ct)
	}
	return nil
}

// Deliver wraps the envelope's bundle as a Bundle.type=message and POSTs
// it to the subscriber's endpoint, returning the classified outcome.
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

// deliverInner runs the wrap → POST → classify pipeline.
func (c *Channel) deliverInner(ctx context.Context, env channel.NotificationEnvelope) channel.DeliveryOutcome {
	parsed, err := url.Parse(env.SubscriptionEndpoint)
	if err != nil {
		return channel.PermanentFailure(fmt.Sprintf("invalid endpoint url: %v", err))
	}
	if parsed.Scheme != "https" {
		c.metrics.Inc(MetricNonHTTPSEndpointTotal, map[string]string{"channel": channelName})
		return channel.PermanentFailure("non-https endpoint")
	}

	wrapStart := time.Now()
	body, err := c.wrapInMessageBundle(env)
	c.metrics.Observe(MetricWrappingDurationSec, time.Since(wrapStart).Seconds(), nil)
	if err != nil {
		c.metrics.Inc(MetricWrapFailureTotal, map[string]string{"channel": channelName})
		// LLD §10 — both inner-Bundle parse failure and MessageHeader
		// assembly failure map to PermanentFailure.
		return channel.PermanentFailure(fmt.Sprintf("wrap: %v", err))
	}

	attemptCtx, cancel := c.applyDeadline(ctx, env.Deadline)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, env.SubscriptionEndpoint, bytes.NewReader(body))
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

// wrapInMessageBundle parses the envelope's inner subscription-notification
// Bundle and produces a Bundle.type="message" whose first entry is a
// MessageHeader. Returns the serialized outer Bundle.
//
// JSON-only for v1: the LLD pseudo-code shows serialize(outer, content_type)
// for fhir+json or fhir+xml, but a FHIR XML serializer is not in tree. v1
// rejects fhir+xml at this seam with an error so the scheduler dead-letters
// the delivery rather than producing malformed wire bytes.
func (c *Channel) wrapInMessageBundle(env channel.NotificationEnvelope) ([]byte, error) {
	if env.ContentType != channel.ContentTypeFHIRJSON {
		return nil, fmt.Errorf("content type %q not supported by message channel in v1 (json only)", env.ContentType)
	}

	var inner struct {
		ResourceType string                   `json:"resourceType"`
		ID           string                   `json:"id,omitempty"`
		Type         string                   `json:"type,omitempty"`
		Entry        []map[string]interface{} `json:"entry,omitempty"`
	}
	if err := json.Unmarshal(env.BundleBytes, &inner); err != nil {
		return nil, fmt.Errorf("inner bundle parse: %w", err)
	}
	if inner.ResourceType != "Bundle" {
		return nil, fmt.Errorf("inner resourceType=%q, want Bundle", inner.ResourceType)
	}
	if len(inner.Entry) == 0 {
		return nil, errors.New("inner bundle has no entries")
	}
	statusType, err := extractSubscriptionStatusType(inner.Entry[0])
	if err != nil {
		return nil, err
	}

	innerID := inner.ID
	innerRef := "Bundle/" + innerID
	if innerID == "" {
		innerID = uuid.NewString()
		innerRef = "urn:uuid:" + innerID
	}

	header := map[string]interface{}{
		"resourceType": "MessageHeader",
		"eventCoding": map[string]interface{}{
			"system": EventCodingSystem,
			"code":   statusType,
		},
		"destination": []map[string]interface{}{
			{"endpoint": env.SubscriptionEndpoint},
		},
		"focus": []map[string]interface{}{
			{"reference": innerRef},
		},
	}
	if c.serverEndpoint != "" {
		header["source"] = map[string]interface{}{"endpoint": c.serverEndpoint}
	}

	outerEntries := make([]map[string]interface{}, 0, len(inner.Entry)+1)
	outerEntries = append(outerEntries, map[string]interface{}{"resource": header})
	outerEntries = append(outerEntries, inner.Entry...)

	outer := map[string]interface{}{
		"resourceType": "Bundle",
		"type":         "message",
		// FHIR `instant` requires sub-second precision. RFC3339 (second
		// precision) parses but mis-shapes the value; use RFC3339Nano so
		// the outer Bundle.timestamp validates cleanly as `instant` (S-5).
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"entry":     outerEntries,
	}

	return json.Marshal(outer)
}

// extractSubscriptionStatusType pulls the SubscriptionStatus.type code off
// entry[0]. The inner Bundle is by spec a subscription-notification whose
// first entry is a SubscriptionStatus.
func extractSubscriptionStatusType(entry map[string]interface{}) (string, error) {
	res, ok := entry["resource"].(map[string]interface{})
	if !ok {
		return "", errors.New("inner entry[0] missing resource")
	}
	if rt, _ := res["resourceType"].(string); rt != "SubscriptionStatus" {
		return "", fmt.Errorf("inner entry[0].resource.resourceType = %q, want SubscriptionStatus", rt)
	}
	t, _ := res["type"].(string)
	if t == "" {
		return "", errors.New("inner SubscriptionStatus.type is empty")
	}
	return t, nil
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
// from LLD §4.1.
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
	return true
}

// validHeaderName matches RFC 7230 token chars, leading-letter required.
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

// classifyHTTPResponse maps an HTTP response to a DeliveryOutcome. Mirrors
// resthook's classifier — "the two channels share the HTTP classifier"
// (LLD §4.4 "Failure mapping").
func (c *Channel) classifyHTTPResponse(resp *http.Response) channel.DeliveryOutcome {
	status := resp.StatusCode
	switch {
	case status >= 200 && status < 300:
		return channel.DeliveryOutcome{Kind: channel.OutcomeDelivered, StatusCode: status}
	case status == http.StatusRequestTimeout:
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     "408 Request Timeout",
			StatusCode: status,
		}
	case status == http.StatusTooManyRequests:
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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return channel.TransientFailure(0, "context canceled: "+err.Error())
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return channel.PermanentFailure("dns nxdomain: " + dnsErr.Name)
		}
		return channel.TransientFailure(0, "dns: "+dnsErr.Err)
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return channel.TransientFailure(0, "network: "+opErr.Op+" "+sanitizeError(opErr.Err))
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return channel.TransientFailure(0, "i/o timeout: "+urlErr.Err.Error())
		}
		return channel.TransientFailure(0, "io: "+urlErr.Err.Error())
	}

	return channel.TransientFailure(0, "io: "+err.Error())
}

// parseRetryAfter parses an RFC 7231 Retry-After header (delta-seconds or
// HTTP-date). Zero means "no hint" — the scheduler applies default backoff.
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
// traceparent. We use the correlation id as the trace-id when present,
// padding/truncating to 32 hex chars. Mirrors resthook so subscribers
// receiving messages from either channel see a uniform shape.
func formatTraceparent(corrID string) string {
	if id, err := correlation.ParseTraceparent(corrID); err == nil {
		return correlation.FormatTraceparent(id)
	}
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
	return strings.TrimSpace(string(buf[:n]))
}

// sanitizeError trims a wrapped error to its message.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

// outcomeLabel renders an OutcomeKind as a metric label.
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

// statusClass renders an HTTP status as a "2xx" / "5xx" / etc. label.
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
// the logging package's PHI redactor.
func (c *Channel) logOutcome(ctx context.Context, env channel.NotificationEnvelope, out channel.DeliveryOutcome) {
	level := slog.LevelInfo
	switch out.Kind {
	case channel.OutcomeTransient:
		level = slog.LevelWarn
	case channel.OutcomePermanent:
		level = slog.LevelError
	}
	c.logger.Log(ctx, level, "message-channel delivery",
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

// compile-time assertion: *Channel satisfies channel.Channel.
var _ channel.Channel = (*Channel)(nil)
