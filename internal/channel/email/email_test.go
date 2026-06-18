// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// fakeMetrics records metric calls for assertions.
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{counters: make(map[string]float64)}
}

func (f *fakeMetrics) Inc(name string, labels map[string]string) {
	f.Add(name, 1, labels)
}
func (f *fakeMetrics) Add(name string, delta float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[keyFor(name, labels)] += delta
}
func (f *fakeMetrics) Observe(string, float64, map[string]string) {}
func (f *fakeMetrics) Set(string, float64, map[string]string)     {}

func (f *fakeMetrics) get(name string, labels map[string]string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[keyFor(name, labels)]
}

func keyFor(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteString("|")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(labels[k])
	}
	return b.String()
}

// newEnvelope constructs a baseline envelope addressed to mailto.
func newEnvelope(rcpt string) channel.NotificationEnvelope {
	return channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             7,
		BundleBytes:          []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              1,
		CorrelationID:        uuid.New().String(),
		SubscriptionEndpoint: "mailto:" + rcpt,
		Deadline:             time.Now().Add(10 * time.Second),
	}
}

// TestNewRejectsSMIMEMode verifies ADR 0010 #5: only SMTP is allowed in v1.
func TestNewRejectsSMIMEMode(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     email.ModeSMIME,
		From:     "noreply@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 25,
	})
	if err == nil {
		t.Fatalf("expected error rejecting smime mode")
	}
	if !strings.Contains(err.Error(), "smime") && !strings.Contains(err.Error(), "ADR 0010") {
		t.Errorf("error should mention smime or ADR 0010: %v", err)
	}
}

func TestNewRejectsDirectMode(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		Mode:     email.ModeDirect,
		From:     "noreply@example.org",
		SMTPHost: "smtp.example.org",
		SMTPPort: 25,
	})
	if err == nil {
		t.Fatalf("expected error rejecting direct mode")
	}
}

func TestNewRequiresFrom(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{SMTPHost: "smtp.example.org", SMTPPort: 25})
	if err == nil {
		t.Fatalf("expected error for missing From")
	}
}

func TestNewRequiresHost(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{From: "a@b.com", SMTPPort: 25})
	if err == nil {
		t.Fatalf("expected error for missing host")
	}
}

func TestNewRequiresValidPort(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{From: "a@b.com", SMTPHost: "h"})
	if err == nil {
		t.Fatalf("expected error for missing port")
	}
}

func TestNewDefaultsModeToSMTP(t *testing.T) {
	t.Parallel()
	ch, err := email.New(email.Config{
		From:     "noreply@example.org",
		SMTPHost: "127.0.0.1",
		SMTPPort: 25,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if ch == nil {
		t.Fatalf("nil channel")
	}
}

func TestNewRejectsBadAuthMechanism(t *testing.T) {
	t.Parallel()
	_, err := email.New(email.Config{
		From:          "a@b.com",
		SMTPHost:      "h",
		SMTPPort:      25,
		AuthMechanism: "OAUTH2",
		AuthUsername:  "u",
	})
	if err == nil {
		t.Fatalf("expected error for unsupported auth mechanism")
	}
}

// TestDeliverInvalidMailtoEndpoint verifies non-mailto endpoints are
// permanently failed without touching the network.
func TestDeliverInvalidMailtoEndpoint(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	env.SubscriptionEndpoint = "https://not-a-mailto.example/"
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

func TestDeliverEmptyEndpoint(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("a@b.com")
	env.SubscriptionEndpoint = ""
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

// TestDeliverSuccessJournalsBundle verifies that successful delivery
// produces a Delivered outcome and that the relay sees the bundle as a
// MIME part.
func TestDeliverSuccessJournalsBundle(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	m := newFakeMetrics()
	ch := newChannelWithMetrics(t, email.Config{
		From:     "Notifier <noreply@example.org>",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	}, m)

	env := newEnvelope("subscriber@example.org")
	out, err := ch.Deliver(context.Background(), env)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}
	if out.StatusCode != 250 {
		t.Errorf("StatusCode = %d, want 250", out.StatusCode)
	}

	msgs := srv.Received()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	got := msgs[0]
	if got.MailFrom != "noreply@example.org" {
		t.Errorf("MAIL FROM = %q, want noreply@example.org", got.MailFrom)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "subscriber@example.org" {
		t.Errorf("RCPT TO = %v", got.Recipients)
	}
	body := string(got.Data)
	if !strings.Contains(body, "From: Notifier <noreply@example.org>") {
		t.Errorf("From header missing: %s", body)
	}
	if !strings.Contains(body, "To: subscriber@example.org") {
		t.Errorf("To header missing: %s", body)
	}
	if !strings.Contains(body, "Content-Type: application/fhir+json") {
		t.Errorf("Content-Type missing: %s", body)
	}
	if !strings.Contains(body, "X-Subscription-Id: ") {
		t.Errorf("X-Subscription-Id missing: %s", body)
	}
	if !strings.Contains(body, "X-Subscription-Event-Number: 7") {
		t.Errorf("X-Subscription-Event-Number missing: %s", body)
	}
	if !strings.Contains(body, `"resourceType":"Bundle"`) {
		t.Errorf("Bundle bytes missing from body: %s", body)
	}

	if got := m.get("fhir_subs_channel_email_deliveries_total",
		map[string]string{"channel": "email", "outcome": "delivered"}); got != 1 {
		t.Errorf("expected 1 delivered counter, got %v", got)
	}
}

// TestDeliverHandshakeOmitsEventNumber verifies non-event-notification
// bundle kinds do not emit X-Subscription-Event-Number.
func TestDeliverHandshakeOmitsEventNumber(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	env.BundleKind = channel.BundleHandshake
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v", out.Kind)
	}
	msgs := srv.Received()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if strings.Contains(string(msgs[0].Data), "X-Subscription-Event-Number") {
		t.Errorf("event number should not be present on handshake: %s", msgs[0].Data)
	}
}

// TestDeliverLargeBundleAttachedAsMIME verifies the attachment path:
// when the bundle exceeds AttachmentThresholdBytes it is included as an
// application/fhir+json attachment with base64 encoding and a text/plain
// summary alongside.
func TestDeliverLargeBundleAttachedAsMIME(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:                     "noreply@example.org",
		SMTPHost:                 srv.Host,
		SMTPPort:                 srv.Port,
		STARTTLS:                 email.STARTTLSDisabled,
		AttachmentThresholdBytes: 32, // tiny so we hit the multipart path.
	})
	env := newEnvelope("subscriber@example.org")
	env.BundleBytes = []byte(`{"resourceType":"Bundle","type":"subscription-notification","entry":[{"fullUrl":"urn:uuid:1","resource":{"resourceType":"SubscriptionStatus","type":"event-notification"}}]}`)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.Reason)
	}
	msgs := srv.Received()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	body := string(msgs[0].Data)
	if !strings.Contains(body, "multipart/mixed") {
		t.Errorf("expected multipart/mixed: %s", body)
	}
	if !strings.Contains(body, "text/plain") {
		t.Errorf("expected text/plain summary: %s", body)
	}
	if !strings.Contains(body, "application/fhir+json") {
		t.Errorf("expected application/fhir+json attachment: %s", body)
	}
	if !strings.Contains(body, `filename="notification.json"`) {
		t.Errorf("expected filename=notification.json: %s", body)
	}
	if !strings.Contains(body, "Content-Transfer-Encoding: base64") {
		t.Errorf("expected base64 attachment: %s", body)
	}
	if !strings.Contains(body, "Content-Disposition: attachment") {
		t.Errorf("expected attachment disposition: %s", body)
	}
	// Summary line is present.
	if !strings.Contains(body, "FHIR Subscription notification") {
		t.Errorf("expected human-readable summary: %s", body)
	}
}

func TestDeliverLargeBundleXMLAttachmentExtension(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:                     "noreply@example.org",
		SMTPHost:                 srv.Host,
		SMTPPort:                 srv.Port,
		STARTTLS:                 email.STARTTLSDisabled,
		AttachmentThresholdBytes: 32,
	})
	env := newEnvelope("subscriber@example.org")
	env.ContentType = channel.ContentTypeFHIRXML
	env.BundleBytes = []byte(`<Bundle xmlns="http://hl7.org/fhir"><type value="subscription-notification"/></Bundle>`)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("kind=%v reason=%q", out.Kind, out.Reason)
	}
	body := string(srv.Received()[0].Data)
	if !strings.Contains(body, `filename="notification.xml"`) {
		t.Errorf("expected notification.xml: %s", body)
	}
	if !strings.Contains(body, "application/fhir+xml") {
		t.Errorf("expected application/fhir+xml: %s", body)
	}
}

// TestSMTP4xxIsTransient verifies a 4xx after MAIL/RCPT/DATA is mapped
// to a TransientFailure.
func TestSMTP4xxIsTransient(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		FailRcpt: &smtpResp{Code: 421, Message: "service unavailable"},
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient, got %v: %q", out.Kind, out.Reason)
	}
	if out.StatusCode != 421 {
		t.Errorf("StatusCode = %d, want 421", out.StatusCode)
	}
}

// TestSMTP5xxIsPermanent verifies a 5xx after MAIL/RCPT/DATA is mapped
// to a PermanentFailure.
func TestSMTP5xxIsPermanent(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		FailRcpt: &smtpResp{Code: 550, Message: "no such mailbox"},
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
	if out.StatusCode != 550 {
		t.Errorf("StatusCode = %d, want 550", out.StatusCode)
	}
}

// TestSMTP5xxOnDataIsPermanent ensures 5xx after the DATA close maps to
// PermanentFailure.
func TestSMTP5xxOnDataIsPermanent(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		FailDataClose: &smtpResp{Code: 552, Message: "message too large"},
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

// TestDialFailureTransient verifies a connect refused is transient.
func TestDialFailureTransient(t *testing.T) {
	t.Parallel()
	// Bind on an ephemeral port, then close to guarantee connect refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = l.Close()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: "127.0.0.1",
		SMTPPort: port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	env.Deadline = time.Now().Add(2 * time.Second)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on connect refused, got %v: %q", out.Kind, out.Reason)
	}
}

// TestSTARTTLSUpgrade verifies the channel upgrades the connection when
// the relay advertises STARTTLS and the policy allows it.
func TestSTARTTLSUpgrade(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		EnableSTARTTLS: true,
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSPreferred,
		TLSConfig: &tls.Config{
			ServerName: srv.Host,
			RootCAs:    srv.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}
	if !srv.LastSessionUsedTLS() {
		t.Errorf("expected the last session to have used TLS")
	}
}

// TestSTARTTLSRequiredButUnsupported is permanent.
func TestSTARTTLSRequiredButUnsupported(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{EnableSTARTTLS: false})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSRequired,
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

// TestPlainAuthSuccess covers the PLAIN AUTH happy path. The fake relay
// allows AUTH PLAIN over TLS only (mirroring real-world relays); we
// upgrade via STARTTLS first.
func TestPlainAuthSuccess(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		EnableSTARTTLS: true,
		AuthUser:       "user",
		AuthPass:       "pass",
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:          "noreply@example.org",
		SMTPHost:      srv.Host,
		SMTPPort:      srv.Port,
		STARTTLS:      email.STARTTLSRequired,
		AuthMechanism: email.AuthPlain,
		AuthUsername:  "user",
		AuthPassword:  "pass",
		TLSConfig: &tls.Config{
			ServerName: srv.Host,
			RootCAs:    srv.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}
	if !srv.LastAuthSucceeded() {
		t.Errorf("expected AUTH to have succeeded on the relay")
	}
}

// TestPlainAuthFailureIsPermanent covers a 535 AUTH failure.
func TestPlainAuthFailureIsPermanent(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		EnableSTARTTLS: true,
		AuthUser:       "user",
		AuthPass:       "pass",
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:          "noreply@example.org",
		SMTPHost:      srv.Host,
		SMTPPort:      srv.Port,
		STARTTLS:      email.STARTTLSRequired,
		AuthMechanism: email.AuthPlain,
		AuthUsername:  "user",
		AuthPassword:  "wrong",
		TLSConfig: &tls.Config{
			ServerName: srv.Host,
			RootCAs:    srv.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent, got %v: %q", out.Kind, out.Reason)
	}
}

// TestLoginAuthSuccess exercises the LOGIN mechanism (custom Auth).
func TestLoginAuthSuccess(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		EnableSTARTTLS:    true,
		AuthUser:          "user",
		AuthPass:          "pass",
		AdvertiseLOGIN:    true,
		HandleLOGINByHand: true,
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:          "noreply@example.org",
		SMTPHost:      srv.Host,
		SMTPPort:      srv.Port,
		STARTTLS:      email.STARTTLSRequired,
		AuthMechanism: email.AuthLogin,
		AuthUsername:  "user",
		AuthPassword:  "pass",
		TLSConfig: &tls.Config{
			ServerName: srv.Host,
			RootCAs:    srv.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	})
	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}
	if !srv.LastAuthSucceeded() {
		t.Errorf("expected LOGIN auth to succeed on relay")
	}
}

// TestConcurrentDeliveriesShareChannel verifies the channel can be
// invoked concurrently without races.
func TestConcurrentDeliveriesShareChannel(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			env := newEnvelope("subscriber@example.org")
			out, err := ch.Deliver(context.Background(), env)
			if err != nil {
				t.Errorf("deliver: %v", err)
				return
			}
			if out.Kind != channel.OutcomeDelivered {
				t.Errorf("kind = %v reason=%q", out.Kind, out.Reason)
			}
		}()
	}
	wg.Wait()
	if got := len(srv.Received()); got != N {
		t.Errorf("relay journaled %d messages, want %d", got, N)
	}
}

// TestContextCanceledDuringDial yields a transient outcome.
func TestContextCanceledDuringDial(t *testing.T) {
	t.Parallel()
	// Listen on a port that accepts but never responds.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			// Hold open, never write the SMTP banner.
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, c)
			_ = c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: "127.0.0.1",
		SMTPPort: port,
		STARTTLS: email.STARTTLSDisabled,
	})
	env := newEnvelope("subscriber@example.org")
	env.Deadline = time.Now().Add(150 * time.Millisecond)
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on slow relay timeout, got %v: %q", out.Kind, out.Reason)
	}
}

// Compile-time check.
var _ channel.Channel = (*email.Channel)(nil)

// --- Test relay -----------------------------------------------------------
//
// A tiny in-process SMTP server that speaks just enough of the protocol
// to drive the channel through its happy + sad paths. We do not depend
// on the e2e mocksub package because that would cross test boundaries.

type smtpResp struct {
	Code    int
	Message string
}

type relayConfig struct {
	EnableSTARTTLS    bool
	AuthUser          string
	AuthPass          string
	AdvertiseLOGIN    bool
	HandleLOGINByHand bool

	// FailRcpt, when non-nil, returns this code on RCPT TO instead of 250.
	FailRcpt *smtpResp
	// FailDataClose, when non-nil, returns this code on DATA close.
	FailDataClose *smtpResp
}

type relayMessage struct {
	MailFrom   string
	Recipients []string
	Data       []byte
	UsedTLS    bool
}

type testRelay struct {
	t        *testing.T
	cfg      relayConfig
	listener net.Listener
	tlsConf  *tls.Config
	RootCAs  *x509.CertPool
	Host     string
	Port     int

	mu       sync.Mutex
	messages []relayMessage
	authOK   bool
	usedTLS  bool

	stopped chan struct{}
}

func startTestRelay(t *testing.T, cfg relayConfig) *testRelay {
	t.Helper()
	tlsConf, pool := generateTestTLS(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcp := l.Addr().(*net.TCPAddr)
	r := &testRelay{
		t:        t,
		cfg:      cfg,
		listener: l,
		tlsConf:  tlsConf,
		RootCAs:  pool,
		Host:     "127.0.0.1",
		Port:     tcp.Port,
		stopped:  make(chan struct{}),
	}
	go r.serve()
	return r
}

func (r *testRelay) Stop() {
	_ = r.listener.Close()
	<-r.stopped
}

func (r *testRelay) Received() []relayMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]relayMessage, len(r.messages))
	copy(out, r.messages)
	return out
}

func (r *testRelay) LastAuthSucceeded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.authOK
}

func (r *testRelay) LastSessionUsedTLS() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usedTLS
}

func (r *testRelay) serve() {
	defer close(r.stopped)
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.handle(conn)
	}
}

// handle implements a tiny SMTP server with the minimum command set our
// tests need: HELO/EHLO, STARTTLS, AUTH PLAIN/LOGIN, MAIL, RCPT, DATA,
// QUIT.
func (r *testRelay) handle(rawConn net.Conn) {
	defer rawConn.Close()
	conn := newSMTPConn(rawConn)
	defer conn.Close()

	if err := conn.WriteLine("220 fake-relay ESMTP"); err != nil {
		return
	}

	var (
		mailFrom      string
		recipients    []string
		usedTLS       bool
		authOK        bool
		needAuth      = r.cfg.AuthUser != ""
		didAuth       bool
		extensions    = ehloExtensions(r.cfg, false)
		extensionsTLS = ehloExtensions(r.cfg, true)
	)

	for {
		line, err := conn.ReadLine()
		if err != nil {
			return
		}
		cmd, args := splitSMTPCommand(line)
		switch cmd {
		case "HELO":
			_ = conn.WriteLine("250 fake-relay")
		case "EHLO":
			ext := extensions
			if usedTLS {
				ext = extensionsTLS
			}
			if err := conn.WriteEHLO("fake-relay", ext); err != nil {
				return
			}
		case "STARTTLS":
			if !r.cfg.EnableSTARTTLS {
				_ = conn.WriteLine("502 STARTTLS not supported")
				continue
			}
			_ = conn.WriteLine("220 Ready to start TLS")
			if err := conn.UpgradeTLS(r.tlsConf); err != nil {
				return
			}
			usedTLS = true
		case "AUTH":
			if !needAuth {
				_ = conn.WriteLine("503 AUTH not advertised")
				continue
			}
			ok := false
			switch {
			case strings.HasPrefix(args, "PLAIN"):
				ok = handleAuthPlain(conn, args, r.cfg.AuthUser, r.cfg.AuthPass)
			case strings.HasPrefix(args, "LOGIN"):
				ok = handleAuthLogin(conn, r.cfg.AuthUser, r.cfg.AuthPass)
			default:
				_ = conn.WriteLine("504 mechanism not implemented")
				continue
			}
			if ok {
				authOK = true
				didAuth = true
				_ = conn.WriteLine("235 Authentication successful")
			} else {
				didAuth = true
				_ = conn.WriteLine("535 Authentication credentials invalid")
			}
		case "MAIL":
			if needAuth && !authOK {
				_ = conn.WriteLine("530 Authentication required")
				continue
			}
			mailFrom = parseAddrParam(args, "FROM:")
			_ = conn.WriteLine("250 OK")
		case "RCPT":
			if r.cfg.FailRcpt != nil {
				_ = conn.WriteLine(formatResp(*r.cfg.FailRcpt))
				continue
			}
			rcpt := parseAddrParam(args, "TO:")
			recipients = append(recipients, rcpt)
			_ = conn.WriteLine("250 OK")
		case "DATA":
			_ = conn.WriteLine("354 Start mail input; end with <CRLF>.<CRLF>")
			body, err := conn.ReadDATA()
			if err != nil {
				return
			}
			if r.cfg.FailDataClose != nil {
				_ = conn.WriteLine(formatResp(*r.cfg.FailDataClose))
			} else {
				r.mu.Lock()
				r.messages = append(r.messages, relayMessage{
					MailFrom:   mailFrom,
					Recipients: append([]string(nil), recipients...),
					Data:       body,
					UsedTLS:    usedTLS,
				})
				r.authOK = authOK
				r.usedTLS = usedTLS
				r.mu.Unlock()
				_ = conn.WriteLine("250 2.0.0 Ok: queued")
			}
			mailFrom = ""
			recipients = nil
		case "RSET":
			mailFrom = ""
			recipients = nil
			_ = conn.WriteLine("250 OK")
		case "NOOP":
			_ = conn.WriteLine("250 OK")
		case "QUIT":
			_ = conn.WriteLine("221 Bye")
			return
		default:
			_ = conn.WriteLine("502 Command not implemented")
		}
		_ = didAuth
	}
}

func ehloExtensions(cfg relayConfig, postTLS bool) []string {
	ext := []string{"PIPELINING", "8BITMIME"}
	if cfg.EnableSTARTTLS && !postTLS {
		ext = append(ext, "STARTTLS")
	}
	if cfg.AuthUser != "" && (postTLS || !cfg.EnableSTARTTLS) {
		mechs := "PLAIN"
		if cfg.AdvertiseLOGIN {
			mechs = "PLAIN LOGIN"
		}
		ext = append(ext, "AUTH "+mechs)
	}
	return ext
}

func handleAuthPlain(conn *smtpConn, args, user, pass string) bool {
	parts := strings.SplitN(args, " ", 2)
	var token string
	if len(parts) == 2 {
		token = parts[1]
	} else {
		_ = conn.WriteLine("334 ")
		line, err := conn.ReadLine()
		if err != nil {
			return false
		}
		token = line
	}
	decoded, err := base64Decode(token)
	if err != nil {
		return false
	}
	// RFC 4616: "[authzid] \0 authcid \0 password"
	parts2 := strings.Split(string(decoded), "\x00")
	if len(parts2) != 3 {
		return false
	}
	return parts2[1] == user && parts2[2] == pass
}

func handleAuthLogin(conn *smtpConn, user, pass string) bool {
	if err := conn.WriteLine("334 " + base64Encode("Username:")); err != nil {
		return false
	}
	uline, err := conn.ReadLine()
	if err != nil {
		return false
	}
	uDecoded, err := base64Decode(uline)
	if err != nil {
		return false
	}
	if writeErr := conn.WriteLine("334 " + base64Encode("Password:")); writeErr != nil {
		return false
	}
	pline, err := conn.ReadLine()
	if err != nil {
		return false
	}
	pDecoded, err := base64Decode(pline)
	if err != nil {
		return false
	}
	return string(uDecoded) == user && string(pDecoded) == pass
}

func splitSMTPCommand(line string) (cmd, args string) {
	line = strings.TrimRight(line, "\r\n")
	if i := strings.IndexByte(line, ' '); i >= 0 {
		return strings.ToUpper(line[:i]), line[i+1:]
	}
	return strings.ToUpper(line), ""
}

func parseAddrParam(args, prefix string) string {
	args = strings.TrimSpace(args)
	upper := strings.ToUpper(args)
	if strings.HasPrefix(upper, prefix) {
		args = args[len(prefix):]
	}
	args = strings.TrimSpace(args)
	args = strings.TrimPrefix(args, "<")
	if i := strings.IndexByte(args, '>'); i >= 0 {
		args = args[:i]
	}
	if i := strings.IndexByte(args, ' '); i >= 0 {
		args = args[:i]
	}
	return args
}

func formatResp(r smtpResp) string {
	if r.Message == "" {
		return rangeCode(r.Code)
	}
	return strings.TrimSpace(strings.Join([]string{
		formatCode(r.Code), r.Message,
	}, " "))
}

func formatCode(code int) string {
	if code <= 0 {
		return "554"
	}
	return integerString(code)
}

func rangeCode(code int) string {
	return formatCode(code)
}

// integerString avoids a strconv import in the hot test helper.
func integerString(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [10]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	s := string(buf[pos:])
	if neg {
		s = "-" + s
	}
	return s
}

// --- helpers --------------------------------------------------------------

// newChannel wraps email.New and fails the test on construction errors.
func newChannel(t *testing.T, cfg email.Config) *email.Channel {
	t.Helper()
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	ch, err := email.New(cfg)
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}
	return ch
}

func newChannelWithMetrics(t *testing.T, cfg email.Config, m *fakeMetrics) *email.Channel {
	t.Helper()
	cfg.Metrics = m
	return newChannel(t, cfg)
}

// Sanity: confirm net/smtp is importable and the version we use produces
// the expected error type. The test does not exercise net/smtp directly;
// it merely fails fast at compile time if the import goes away.
var _ = smtp.PlainAuth
