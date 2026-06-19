// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// Phase A — RED tests for OP #114 (email scope reduction): the email
// channel must expose a real RCPT-TO probe that drives a live SMTP
// session against the configured relay and classifies the outcome.
// These tests fail today because Channel.ProbeRecipient does not exist.

// TestProbeRecipient_Accepted asserts a 250 OK on RCPT TO produces
// ProbeAccepted. The probe exercises the real SMTP path: dial -> EHLO ->
// MAIL FROM -> RCPT TO -> RSET -> QUIT. No DATA frame is sent, so the
// relay never receives a body — the activator only validates that the
// recipient mailbox is acceptable to the relay.
func TestProbeRecipient_Accepted(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})

	out, err := ch.ProbeRecipient(context.Background(), "mailto:subscriber@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeAccepted {
		t.Fatalf("Outcome = %v (%q); want ProbeAccepted", out.Outcome, out.Reason)
	}
	if out.StatusCode != 250 {
		t.Errorf("StatusCode = %d; want 250", out.StatusCode)
	}
	// The probe MUST NOT submit DATA — the relay must record zero
	// messages even though we made it through RCPT TO.
	if got := len(srv.Received()); got != 0 {
		t.Errorf("relay received %d messages; want 0 (RCPT-TO probe must not submit DATA)", got)
	}
}

// TestProbeRecipient_5xxRejected asserts a 5xx response on RCPT TO
// (e.g., "550 No such user") produces ProbeRejected — the mailbox does
// not exist and retrying creation will not change that.
func TestProbeRecipient_5xxRejected(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		FailRcpt: &smtpResp{Code: 550, Message: "No such user here"},
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})

	out, err := ch.ProbeRecipient(context.Background(), "mailto:nobody@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeRejected {
		t.Fatalf("Outcome = %v (%q); want ProbeRejected", out.Outcome, out.Reason)
	}
	if out.StatusCode != 550 {
		t.Errorf("StatusCode = %d; want 550", out.StatusCode)
	}
}

// TestProbeRecipient_4xxTransient asserts a 4xx response on RCPT TO
// (e.g., "451 graylist") produces ProbeTransient — the relay is
// temporarily refusing and the operator can retry. The activator caller
// (handlers.activate) will treat ProbeTransient as HandshakeFailed for
// row-state purposes; the metric label distinguishes 4xx from 5xx for
// alerting.
func TestProbeRecipient_4xxTransient(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{
		FailRcpt: &smtpResp{Code: 451, Message: "graylisted"},
	})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})

	out, err := ch.ProbeRecipient(context.Background(), "mailto:slow@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeTransient {
		t.Fatalf("Outcome = %v (%q); want ProbeTransient", out.Outcome, out.Reason)
	}
	if out.StatusCode != 451 {
		t.Errorf("StatusCode = %d; want 451", out.StatusCode)
	}
}

// TestProbeRecipient_DialFailureTransient asserts a connection failure
// (closed port) maps to ProbeTransient. The relay may be momentarily
// unavailable; the operator can retry.
func TestProbeRecipient_DialFailureTransient(t *testing.T) {
	t.Parallel()
	ch := newChannel(t, email.Config{
		From:           "noreply@example.org",
		SMTPHost:       "127.0.0.1",
		SMTPPort:       1, // closed port
		STARTTLS:       email.STARTTLSDisabled,
		RequestTimeout: 1 * time.Second,
	})

	out, err := ch.ProbeRecipient(context.Background(), "mailto:subscriber@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeTransient {
		t.Fatalf("Outcome = %v (%q); want ProbeTransient", out.Outcome, out.Reason)
	}
	if out.StatusCode != 0 {
		t.Errorf("StatusCode = %d; want 0 for transport failure", out.StatusCode)
	}
}

// TestProbeRecipient_InvalidEndpointRejected asserts a non-mailto:
// endpoint is rejected as ProbeRejected without touching the network.
// The activator must classify malformed endpoints as terminal so the
// API row does not stay stuck at "requested".
func TestProbeRecipient_InvalidEndpointRejected(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
	})

	out, err := ch.ProbeRecipient(context.Background(), "https://not-a-mailto.example/")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeRejected {
		t.Fatalf("Outcome = %v (%q); want ProbeRejected", out.Outcome, out.Reason)
	}
	if !strings.Contains(out.Reason, "mailto") {
		t.Errorf("Reason should mention mailto; got %q", out.Reason)
	}
}

// TestProbeRecipient_STARTTLSRequiredButNotOffered asserts that a relay
// that does not advertise STARTTLS while the channel is configured
// STARTTLSRequired produces ProbeRejected. The relay does not meet the
// channel's TLS policy and retrying will not help.
func TestProbeRecipient_STARTTLSRequiredButNotOffered(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{EnableSTARTTLS: false})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSRequired,
	})

	out, err := ch.ProbeRecipient(context.Background(), "mailto:subscriber@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeRejected {
		t.Fatalf("Outcome = %v (%q); want ProbeRejected", out.Outcome, out.Reason)
	}
}

// TestProbeRecipient_DeadlineRespected asserts that a context deadline
// caps the probe — a hung relay must not pin the activation goroutine
// past the deadline. Uses a context that is already expired.
func TestProbeRecipient_DeadlineRespected(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:           "noreply@example.org",
		SMTPHost:       srv.Host,
		SMTPPort:       srv.Port,
		STARTTLS:       email.STARTTLSDisabled,
		RequestTimeout: 5 * time.Second,
	})

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()
	out, err := ch.ProbeRecipient(ctx, "mailto:subscriber@example.org")
	if err != nil {
		t.Fatalf("ProbeRecipient: %v", err)
	}
	if out.Outcome != email.ProbeTransient {
		t.Errorf("Outcome = %v (%q); want ProbeTransient on expired deadline", out.Outcome, out.Reason)
	}
}
