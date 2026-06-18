// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package email implements the email notification channel: SMTP submission
// of a multipart MIME message that carries a serialized FHIR
// subscription-notification Bundle as an application/fhir+json (or +xml)
// MIME part, alongside a human-readable text/plain summary.
//
// Per docs/low-level-design/channels.md §4.3 and ADR 0010 #5, v1 ships
// SMTP-only: no S/MIME, no Direct SMTP, no DKIM signing. Those are
// explicit v2 follow-ups. The channel uses Go stdlib net/smtp; no third-
// party SMTP client.
//
// Like every Channel SPI implementation, this channel is responsible for
// protocol-level behavior (MIME assembly, STARTTLS, AUTH, response
// classification) within one delivery attempt. It does NOT own cross-
// attempt retry, dead-lettering, or row writes to the deliveries table —
// those are the scheduler's responsibility. The channel returns a typed
// DeliveryOutcome and trusts the scheduler to translate it to the right
// database transition.
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
)

// Channel name constant; used in metric labels and log fields.
const channelName = "email"

// Mode selects the email delivery mode. Per ADR 0010 #5, v1 ships SMTP
// only. The other values are documented but cause a configuration error.
type Mode string

// Mode values.
const (
	ModeSMTP   Mode = "smtp"
	ModeSMIME  Mode = "smime"
	ModeDirect Mode = "direct"
)

// AuthMechanism selects the SMTP AUTH mechanism. Stdlib net/smtp supports
// PLAIN, LOGIN (via a custom Auth), and CRAM-MD5 directly.
type AuthMechanism string

// AuthMechanism values.
const (
	AuthNone    AuthMechanism = ""
	AuthPlain   AuthMechanism = "PLAIN"
	AuthLogin   AuthMechanism = "LOGIN"
	AuthCRAMMD5 AuthMechanism = "CRAM-MD5"
)

// STARTTLSPolicy controls whether STARTTLS is required, preferred, or
// disabled.
type STARTTLSPolicy string

// STARTTLSPolicy values.
const (
	// STARTTLSRequired refuses to send if the relay does not advertise
	// STARTTLS. Submission upgrades the connection before MAIL FROM.
	STARTTLSRequired STARTTLSPolicy = "required"
	// STARTTLSPreferred upgrades when offered, falls back to plaintext
	// when not. Default for backward compatibility with legacy relays.
	STARTTLSPreferred STARTTLSPolicy = "preferred"
	// STARTTLSDisabled never upgrades.
	STARTTLSDisabled STARTTLSPolicy = "disabled"
)

// Default values. These mirror the defaults in
// docs/low-level-design/channels.md §4.3.
const (
	DefaultRequestTimeout          = 30 * time.Second
	DefaultAttachmentThresholdByte = 65536
	DefaultUserAgent               = "fhir-subscriptions-foss/0.0"
	DefaultSubjectTemplate         = "FHIR Subscription notification"
	mimeBoundaryPrefix             = "fhir-subs-"
)

// Metric names emitted by this channel. Wire form follows the
// fhir_subs_channel_email_* convention from the operating procedure
// and architecture.md §observability.
const (
	MetricDeliveriesTotal     = "fhir_subs_channel_email_deliveries_total"
	MetricDeliveryDurationSec = "fhir_subs_channel_email_delivery_duration_seconds"
	MetricSMTPResponsesTotal  = "fhir_subs_channel_email_smtp_responses_total"
	MetricSMTPPoolInUse       = "fhir_subs_channel_email_smtp_pool_in_use"
)

// Config configures a Channel at construction time. Zero values fall
// back to the package defaults documented above.
type Config struct {
	// Mode is the delivery mode. Must be ModeSMTP in v1; ModeSMIME and
	// ModeDirect are rejected by New per ADR 0010 #5.
	Mode Mode

	// From is the RFC 5322 From: address that appears on every email
	// the channel sends. Required.
	From string

	// SubjectTemplate is the subject line. Empty falls back to
	// DefaultSubjectTemplate. The string is currently used verbatim;
	// future revisions may add template substitution.
	SubjectTemplate string

	// SMTPHost is the relay host name (or IP). Required.
	SMTPHost string
	// SMTPPort is the relay port. Common values: 25 (relay), 465
	// (implicit TLS, not supported in v1), 587 (submission +
	// STARTTLS), 2525 (submission alternate). Required.
	SMTPPort int

	// STARTTLS controls TLS upgrade behavior. Default
	// STARTTLSPreferred when zero.
	STARTTLS STARTTLSPolicy

	// AuthMechanism selects the AUTH method. AuthNone disables AUTH.
	AuthMechanism AuthMechanism
	// AuthUsername is the AUTH username. Required when AuthMechanism
	// is non-empty.
	AuthUsername string
	// AuthPassword is the AUTH password / shared secret.
	AuthPassword string
	// AuthIdentity is the optional PLAIN-mode identity. Empty for the
	// usual "username = identity" case.
	AuthIdentity string

	// AttachmentThresholdBytes is the size at which the channel
	// switches from inline body to multipart/mixed with the bundle as
	// an attached MIME part. Zero falls back to
	// DefaultAttachmentThresholdByte.
	AttachmentThresholdBytes int

	// RequestTimeout is the per-attempt total wall-clock budget
	// applied when envelope.Deadline is zero. Zero falls back to
	// DefaultRequestTimeout.
	RequestTimeout time.Duration

	// TLSConfig overrides the default TLS configuration used during
	// STARTTLS. Tests use this to trust a self-signed certificate.
	TLSConfig *tls.Config

	// LocalName overrides the EHLO / HELO name sent to the relay.
	// Empty defaults to "localhost".
	LocalName string

	// UserAgent is the value of the X-Mailer header. Empty falls back
	// to DefaultUserAgent.
	UserAgent string

	// Metrics receives counter increments and histogram observations.
	// nil falls back to channel.NopMetrics.
	Metrics channel.MetricsEmitter
	// Logger is the structured logger. nil falls back to slog.Default().
	Logger *slog.Logger

	// dialFunc is an optional override that returns a connected SMTP
	// client. Tests use this to skip TLS bring-up by passing a plain
	// connection. Production callers leave it nil.
	dialFunc func(ctx context.Context, host string, port int, tlsConf *tls.Config, policy STARTTLSPolicy, localName string) (*smtp.Client, error)
}

// Channel implements the email delivery channel. Construct with New;
// Channel is safe for concurrent use.
type Channel struct {
	cfg     Config
	metrics channel.MetricsEmitter
	logger  *slog.Logger
}

// New constructs an email Channel. Returns an error when the
// configuration is invalid (S/MIME or Direct mode requested, missing
// host/port/from, etc).
func New(cfg Config) (*Channel, error) {
	if cfg.Mode == "" {
		cfg.Mode = ModeSMTP
	}
	switch cfg.Mode {
	case ModeSMTP:
		// ok
	case ModeSMIME, ModeDirect:
		return nil, fmt.Errorf("email: mode %q is not supported in v1; only %q is shipped (see ADR 0010 #5)", cfg.Mode, ModeSMTP)
	default:
		return nil, fmt.Errorf("email: unknown mode %q", cfg.Mode)
	}
	if strings.TrimSpace(cfg.From) == "" {
		return nil, errors.New("email: From is required")
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		return nil, fmt.Errorf("email: From is not a valid RFC 5322 address: %w", err)
	}
	if strings.TrimSpace(cfg.SMTPHost) == "" {
		return nil, errors.New("email: SMTPHost is required")
	}
	if cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
		return nil, fmt.Errorf("email: SMTPPort %d out of range", cfg.SMTPPort)
	}
	if cfg.STARTTLS == "" {
		cfg.STARTTLS = STARTTLSPreferred
	}
	switch cfg.STARTTLS {
	case STARTTLSRequired, STARTTLSPreferred, STARTTLSDisabled:
	default:
		return nil, fmt.Errorf("email: unknown STARTTLS policy %q", cfg.STARTTLS)
	}
	if cfg.AuthMechanism != AuthNone {
		switch cfg.AuthMechanism {
		case AuthPlain, AuthLogin, AuthCRAMMD5:
		default:
			return nil, fmt.Errorf("email: unsupported AuthMechanism %q", cfg.AuthMechanism)
		}
		if cfg.AuthUsername == "" {
			return nil, errors.New("email: AuthUsername required for non-empty AuthMechanism")
		}
	}
	if cfg.AttachmentThresholdBytes <= 0 {
		cfg.AttachmentThresholdBytes = DefaultAttachmentThresholdByte
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.SubjectTemplate == "" {
		cfg.SubjectTemplate = DefaultSubjectTemplate
	}
	if cfg.LocalName == "" {
		cfg.LocalName = "localhost"
	}
	metrics := cfg.Metrics
	if metrics == nil {
		metrics = channel.NopMetrics{}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.dialFunc == nil {
		cfg.dialFunc = defaultDial
	}
	return &Channel{cfg: cfg, metrics: metrics, logger: logger}, nil
}

// Deliver assembles a MIME message carrying the envelope's bundle and
// submits it to the configured SMTP relay. The SMTP response is
// classified into a DeliveryOutcome.
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

// deliverInner does the work and returns the classified outcome.
func (c *Channel) deliverInner(ctx context.Context, env channel.NotificationEnvelope) channel.DeliveryOutcome {
	rcpt, err := parseMailto(env.SubscriptionEndpoint)
	if err != nil {
		return channel.PermanentFailure(fmt.Sprintf("invalid mailto endpoint: %v", err))
	}

	mimeBytes, err := c.buildMIME(env, rcpt)
	if err != nil {
		return channel.PermanentFailure(fmt.Sprintf("build mime: %v", err))
	}

	attemptCtx, cancel := c.applyDeadline(ctx, env.Deadline)
	defer cancel()

	client, err := c.cfg.dialFunc(attemptCtx, c.cfg.SMTPHost, c.cfg.SMTPPort, c.cfg.TLSConfig, c.cfg.STARTTLS, c.cfg.LocalName)
	if err != nil {
		return classifyDialError(err)
	}
	defer func() {
		_ = client.Close()
	}()

	if c.cfg.AuthMechanism != AuthNone {
		auth, authErr := buildAuth(c.cfg)
		if authErr != nil {
			return channel.PermanentFailure(fmt.Sprintf("auth setup: %v", authErr))
		}
		if authClientErr := client.Auth(auth); authClientErr != nil {
			return classifyAuthError(authClientErr)
		}
	}

	if mailErr := client.Mail(addressOnly(c.cfg.From)); mailErr != nil {
		return classifySMTPError(mailErr, "MAIL FROM")
	}
	if rcptErr := client.Rcpt(rcpt); rcptErr != nil {
		return classifySMTPError(rcptErr, "RCPT TO")
	}
	wc, dataErr := client.Data()
	if dataErr != nil {
		return classifySMTPError(dataErr, "DATA")
	}
	if _, writeErr := wc.Write(mimeBytes); writeErr != nil {
		// A write error after DATA is mid-protocol; transient and the
		// scheduler can retry.
		return channel.TransientFailure(0, fmt.Sprintf("write data: %v", writeErr))
	}
	if closeErr := wc.Close(); closeErr != nil {
		return classifySMTPError(closeErr, "DATA close")
	}
	// Quit is best-effort; the relay accepted DATA already.
	_ = client.Quit()

	c.metrics.Inc(MetricSMTPResponsesTotal, map[string]string{
		"channel": channelName,
		"class":   "2xx",
	})
	return channel.DeliveryOutcome{Kind: channel.OutcomeDelivered, StatusCode: 250}
}

// applyDeadline derives an attempt context from ctx and the envelope's
// deadline (or this channel's default timeout).
func (c *Channel) applyDeadline(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	if !deadline.IsZero() {
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithTimeout(ctx, c.cfg.RequestTimeout)
}

// parseMailto extracts the bare email address from a mailto: URI.
// Returns an error for any other scheme or malformed input.
func parseMailto(endpoint string) (string, error) {
	if endpoint == "" {
		return "", errors.New("empty endpoint")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme != "mailto" {
		return "", fmt.Errorf("scheme %q (want mailto)", u.Scheme)
	}
	addr := u.Opaque
	if addr == "" {
		// Some parsers put it on Path.
		addr = u.Path
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("missing address in mailto URI")
	}
	if _, err := mail.ParseAddress(addr); err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	return addr, nil
}

// addressOnly returns just the address portion of an RFC 5322 address.
// "Brian <a@b.com>" -> "a@b.com". Falls back to the input on parse failure.
func addressOnly(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

// buildMIME composes the MIME message body for one envelope. When the
// bundle fits under the attachment threshold, the bundle is the inline
// body. When it exceeds the threshold (typical for full-resource
// payloads) the message is multipart/mixed with a text/plain summary
// and the bundle as a base64-encoded attachment.
func (c *Channel) buildMIME(env channel.NotificationEnvelope, rcpt string) ([]byte, error) {
	subject := c.cfg.SubjectTemplate
	msgID, err := newMessageID(env.CorrelationID, c.cfg.From)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	writeHeader(&buf, "From", c.cfg.From)
	writeHeader(&buf, "To", rcpt)
	writeHeader(&buf, "Subject", mime.QEncoding.Encode("utf-8", subject))
	writeHeader(&buf, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeHeader(&buf, "Message-ID", msgID)
	writeHeader(&buf, "MIME-Version", "1.0")
	writeHeader(&buf, "X-Mailer", c.cfg.UserAgent)
	if env.CorrelationID != "" {
		writeHeader(&buf, "X-Correlation-ID", env.CorrelationID)
	}
	writeHeader(&buf, "X-Subscription-Id", env.SubscriptionID.String())
	if env.BundleKind == channel.BundleEventNotification {
		writeHeader(&buf, "X-Subscription-Event-Number", fmt.Sprintf("%d", env.Sequence))
	}

	bundleCT := string(env.ContentType)
	if bundleCT == "" {
		bundleCT = string(channel.ContentTypeFHIRJSON)
	}

	if len(env.BundleBytes) <= c.cfg.AttachmentThresholdBytes {
		// Inline body.
		writeHeader(&buf, "Content-Type", bundleCT+`; charset="utf-8"`)
		writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
		buf.WriteString("\r\n")
		buf.Write(env.BundleBytes)
		// Ensure trailing CRLF for SMTP.
		if !bytes.HasSuffix(env.BundleBytes, []byte("\r\n")) {
			buf.WriteString("\r\n")
		}
		return buf.Bytes(), nil
	}

	// multipart/mixed with summary + attachment.
	boundary, err := newBoundary()
	if err != nil {
		return nil, err
	}
	writeHeader(&buf, "Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	buf.WriteString("\r\n")
	buf.WriteString("This is a multipart message in MIME format.\r\n")

	// Part 1: text/plain summary.
	buf.WriteString("--" + boundary + "\r\n")
	writeHeader(&buf, "Content-Type", `text/plain; charset="utf-8"`)
	writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
	buf.WriteString("\r\n")
	summary := buildSummary(env)
	buf.WriteString(summary)
	buf.WriteString("\r\n")

	// Part 2: bundle as application/fhir+json (or +xml) attachment.
	buf.WriteString("--" + boundary + "\r\n")
	filename := "notification.json"
	if env.ContentType == channel.ContentTypeFHIRXML {
		filename = "notification.xml"
	}
	writeHeader(&buf, "Content-Type", bundleCT+`; name="`+filename+`"`)
	writeHeader(&buf, "Content-Disposition", `attachment; filename="`+filename+`"`)
	writeHeader(&buf, "Content-Transfer-Encoding", "base64")
	buf.WriteString("\r\n")
	writeBase64(&buf, env.BundleBytes)
	buf.WriteString("\r\n")

	buf.WriteString("--" + boundary + "--\r\n")
	return buf.Bytes(), nil
}

// buildSummary renders a short human-readable text summary of the
// notification. PHI is intentionally not included — only metadata.
func buildSummary(env channel.NotificationEnvelope) string {
	var b strings.Builder
	b.WriteString("FHIR Subscription notification\r\n")
	b.WriteString("Subscription: ")
	b.WriteString(env.SubscriptionID.String())
	b.WriteString("\r\n")
	b.WriteString("Notification type: ")
	b.WriteString(string(env.BundleKind))
	b.WriteString("\r\n")
	if env.BundleKind == channel.BundleEventNotification {
		b.WriteString(fmt.Sprintf("Event number: %d\r\n", env.Sequence))
	}
	if env.PayloadType != "" {
		b.WriteString("Payload: ")
		b.WriteString(string(env.PayloadType))
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString("The full notification Bundle is attached as ")
	b.WriteString(string(env.ContentType))
	b.WriteString(".\r\n")
	return b.String()
}

// writeHeader writes a single MIME header line followed by CRLF.
func writeHeader(w *bytes.Buffer, name, value string) {
	w.WriteString(name)
	w.WriteString(": ")
	w.WriteString(value)
	w.WriteString("\r\n")
}

// writeBase64 writes value as a base64-encoded body, line-folded at 76
// chars per RFC 2045.
func writeBase64(w *bytes.Buffer, value []byte) {
	enc := base64.StdEncoding.EncodeToString(value)
	const lineLen = 76
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		w.WriteString(enc[i:end])
		w.WriteString("\r\n")
	}
}

// newMessageID returns a Message-ID header value of the form
// "<correlation-id@host>". The host is taken from the configured From.
func newMessageID(corr, from string) (string, error) {
	host := "localhost"
	if a, err := mail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(a.Address, "@"); at >= 0 {
			host = a.Address[at+1:]
		}
	}
	if corr == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		corr = base64.RawURLEncoding.EncodeToString(b[:])
	}
	return "<" + corr + "@" + host + ">", nil
}

// newBoundary returns a fresh MIME multipart boundary.
func newBoundary() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return mimeBoundaryPrefix + base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// defaultDial dials the relay, runs EHLO, optionally upgrades via
// STARTTLS, and returns the prepared client. The caller is responsible
// for closing the returned client.
func defaultDial(ctx context.Context, host string, port int, tlsConf *tls.Config, policy STARTTLSPolicy, localName string) (*smtp.Client, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	dialer := &net.Dialer{}
	if d, ok := ctx.Deadline(); ok {
		dialer.Deadline = d
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// Apply the deadline at the socket level so EHLO / STARTTLS / AUTH
	// inherit the wall-clock budget.
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := client.Hello(localName); err != nil {
		_ = client.Close()
		return nil, err
	}
	switch policy {
	case STARTTLSDisabled:
		// No upgrade.
	case STARTTLSRequired, STARTTLSPreferred:
		if ok, _ := client.Extension("STARTTLS"); ok {
			tc := tlsConf
			if tc == nil {
				tc = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
			} else if tc.ServerName == "" {
				tc = tc.Clone()
				tc.ServerName = host
			}
			if err := client.StartTLS(tc); err != nil {
				_ = client.Close()
				return nil, fmt.Errorf("starttls: %w", err)
			}
			// Re-EHLO after STARTTLS so capability list reflects the
			// authenticated extensions (per RFC 3207). net/smtp does
			// this internally on StartTLS, but we explicitly Hello
			// again to keep behavior obvious.
		} else if policy == STARTTLSRequired {
			_ = client.Close()
			return nil, errors.New("starttls required by config but relay does not advertise it")
		}
	}
	return client, nil
}

// buildAuth constructs the configured smtp.Auth.
func buildAuth(cfg Config) (smtp.Auth, error) {
	switch cfg.AuthMechanism {
	case AuthPlain:
		return smtp.PlainAuth(cfg.AuthIdentity, cfg.AuthUsername, cfg.AuthPassword, cfg.SMTPHost), nil
	case AuthLogin:
		return loginAuth{username: cfg.AuthUsername, password: cfg.AuthPassword, host: cfg.SMTPHost}, nil
	case AuthCRAMMD5:
		return smtp.CRAMMD5Auth(cfg.AuthUsername, cfg.AuthPassword), nil
	default:
		return nil, fmt.Errorf("unsupported auth mechanism %q", cfg.AuthMechanism)
	}
}

// loginAuth implements smtp.Auth for the LOGIN mechanism. Stdlib
// net/smtp does not ship a LOGIN implementation but most relays accept
// it; the protocol is two base64-encoded prompts.
type loginAuth struct {
	username, password, host string
}

func (a loginAuth) Start(server *smtp.ServerInfo) (proto string, ir []byte, err error) {
	if !server.TLS && server.Name != "localhost" && server.Name != "127.0.0.1" {
		// LOGIN sends the password in the clear; refuse over an
		// unencrypted connection except for local relays.
		return "", nil, errors.New("smtp: LOGIN requires TLS")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	default:
		return nil, fmt.Errorf("smtp LOGIN: unexpected server prompt %q", fromServer)
	}
}

// classifyDialError maps a connection-establishment failure to a
// DeliveryOutcome. All connection errors are transient — the relay may
// be momentarily unavailable.
func classifyDialError(err error) channel.DeliveryOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return channel.TransientFailure(0, "context canceled: "+err.Error())
	}
	// "starttls required by config but relay does not advertise it"
	// is permanent: the relay is misconfigured for our policy and
	// retrying will not change that.
	if err != nil && strings.Contains(err.Error(), "starttls required by config") {
		return channel.PermanentFailure(err.Error())
	}
	return channel.TransientFailure(0, "dial: "+err.Error())
}

// classifySMTPError maps an SMTP protocol error to a DeliveryOutcome.
// The mapping is the standard 4xx -> Transient, 5xx -> Permanent rule
// from docs/low-level-design/channels.md §4.3.
func classifySMTPError(err error, op string) channel.DeliveryOutcome {
	code := smtpErrorCode(err)
	if code == 0 {
		// Non-protocol error (network, TLS read, etc.) — transient.
		return channel.TransientFailure(0, fmt.Sprintf("%s: %v", op, err))
	}
	switch {
	case code >= 200 && code < 300:
		// Should not normally surface as an error, but treat as delivered.
		return channel.DeliveryOutcome{Kind: channel.OutcomeDelivered, StatusCode: code}
	case code >= 400 && code < 500:
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomeTransient,
			Reason:     fmt.Sprintf("%s: %d %s", op, code, smtpErrorMessage(err)),
			StatusCode: code,
		}
	case code >= 500 && code < 600:
		return channel.DeliveryOutcome{
			Kind:       channel.OutcomePermanent,
			Reason:     fmt.Sprintf("%s: %d %s", op, code, smtpErrorMessage(err)),
			StatusCode: code,
		}
	default:
		return channel.TransientFailure(0, fmt.Sprintf("%s: unrecognized smtp code %d %s", op, code, smtpErrorMessage(err)))
	}
}

// classifyAuthError wraps classifySMTPError with AUTH-specific framing.
// 5xx auth failures are permanent.
func classifyAuthError(err error) channel.DeliveryOutcome {
	out := classifySMTPError(err, "AUTH")
	if out.Kind == channel.OutcomeTransient {
		// Most auth failures are 5xx; keep transient when 4xx.
		return out
	}
	if out.Kind == channel.OutcomePermanent {
		return channel.PermanentFailure(out.Reason)
	}
	return out
}

// smtpErrorCode pulls the numeric code off an SMTP error from net/smtp.
// net/smtp returns errors of type *smtp.SMTPError for protocol
// responses, but exposes Code/Message via field access. We treat any
// error matching that shape as protocol-level.
func smtpErrorCode(err error) int {
	if err == nil {
		return 0
	}
	// net/smtp.Error is the documented type for protocol responses.
	type smtpErr interface{ error }
	_ = smtpErr(err)

	// The concrete type is *textproto.Error wrapped, OR *smtp.Error.
	// We avoid importing textproto by parsing the leading number off
	// the Error() string.
	s := err.Error()
	s = strings.TrimSpace(s)
	if len(s) < 3 {
		return 0
	}
	// Skip a leading "smtp: " prefix net/smtp adds.
	if strings.HasPrefix(s, "smtp: ") {
		s = strings.TrimPrefix(s, "smtp: ")
	}
	// Parse the leading 3-digit code.
	if len(s) < 3 {
		return 0
	}
	if !isDigit(s[0]) || !isDigit(s[1]) || !isDigit(s[2]) {
		return 0
	}
	code := int(s[0]-'0')*100 + int(s[1]-'0')*10 + int(s[2]-'0')
	return code
}

// smtpErrorMessage strips the leading code and prefixes from the
// canonical error string and returns the human portion.
func smtpErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	s = strings.TrimPrefix(s, "smtp: ")
	if len(s) >= 4 && isDigit(s[0]) && isDigit(s[1]) && isDigit(s[2]) && (s[3] == ' ' || s[3] == '-') {
		return strings.TrimSpace(s[4:])
	}
	return s
}

// isDigit reports whether b is an ASCII digit.
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

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

// logOutcome emits one structured log line per delivery attempt.
func (c *Channel) logOutcome(ctx context.Context, env channel.NotificationEnvelope, out channel.DeliveryOutcome) {
	level := slog.LevelInfo
	switch out.Kind {
	case channel.OutcomeTransient:
		level = slog.LevelWarn
	case channel.OutcomePermanent:
		level = slog.LevelError
	}
	c.logger.Log(ctx, level, "email delivery",
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

// Compile-time check that *Channel satisfies channel.Channel.
var _ channel.Channel = (*Channel)(nil)
