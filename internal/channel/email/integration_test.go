// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestIntegrationFullSubmissionPath drives one delivery through the full
// happy path: STARTTLS upgrade, PLAIN AUTH, MIME assembly with the
// bundle attached as application/fhir+json, SMTP 250, journaled
// delivery on the relay side. Mirrors the resthook integration test in
// shape — one comprehensive scenario over the real wire.
func TestIntegrationFullSubmissionPath(t *testing.T) {
	t.Parallel()

	srv := startTestRelay(t, relayConfig{
		EnableSTARTTLS: true,
		AuthUser:       "ops",
		AuthPass:       "s3cret",
	})
	defer srv.Stop()

	m := newFakeMetrics()
	ch := newChannelWithMetrics(t, email.Config{
		From:                     "Notifier <noreply@example.org>",
		SubjectTemplate:          "FHIR Subscription notification: order-changed",
		SMTPHost:                 srv.Host,
		SMTPPort:                 srv.Port,
		STARTTLS:                 email.STARTTLSRequired,
		AuthMechanism:            email.AuthPlain,
		AuthUsername:             "ops",
		AuthPassword:             "s3cret",
		AttachmentThresholdBytes: 64,
		TLSConfig: &tls.Config{
			ServerName: srv.Host,
			RootCAs:    srv.RootCAs,
			MinVersion: tls.VersionTLS12,
		},
	}, m)

	corr := uuid.NewString()
	subID := uuid.New()
	bundle := []byte(`{"resourceType":"Bundle","type":"subscription-notification","entry":[{"fullUrl":"urn:uuid:1","resource":{"resourceType":"SubscriptionStatus","status":"active","type":"event-notification","eventsSinceSubscriptionStart":"42"}},{"fullUrl":"ServiceRequest/abc","resource":{"resourceType":"ServiceRequest","status":"active","intent":"order"}}]}`)

	env := channel.NotificationEnvelope{
		SubscriptionID:       subID,
		Sequence:             42,
		BundleBytes:          bundle,
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadFullResource,
		ContentType:          channel.ContentTypeFHIRJSON,
		Attempt:              0,
		CorrelationID:        corr,
		SubscriptionEndpoint: "mailto:subscriber@example.org",
		Deadline:             time.Now().Add(5 * time.Second),
	}

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

	if !srv.LastSessionUsedTLS() {
		t.Errorf("expected STARTTLS to have upgraded the connection")
	}
	if !srv.LastAuthSucceeded() {
		t.Errorf("expected AUTH PLAIN to have succeeded on the relay")
	}

	msgs := srv.Received()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 journaled message, got %d", len(msgs))
	}
	got := msgs[0]
	if got.MailFrom != "noreply@example.org" {
		t.Errorf("MAIL FROM = %q, want noreply@example.org", got.MailFrom)
	}
	if len(got.Recipients) != 1 || got.Recipients[0] != "subscriber@example.org" {
		t.Errorf("recipients = %v", got.Recipients)
	}
	if !got.UsedTLS {
		t.Errorf("relay reports the message was not received over TLS")
	}
	body := string(got.Data)

	// Mandatory MIME headers.
	for _, want := range []string{
		"From: Notifier <noreply@example.org>",
		"To: subscriber@example.org",
		"Subject: ", // we use Q-encoding; just ensure it's present
		"X-Subscription-Id: " + subID.String(),
		"X-Subscription-Event-Number: 42",
		"X-Correlation-ID: " + corr,
		"MIME-Version: 1.0",
		"multipart/mixed",
		"text/plain",
		`application/fhir+json; name="notification.json"`,
		`Content-Disposition: attachment; filename="notification.json"`,
		"Content-Transfer-Encoding: base64",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing header / part %q\n--- body ---\n%s", want, body)
		}
	}

	// Counter assertions.
	if got := m.get("fhir_subs_channel_email_deliveries_total",
		map[string]string{"channel": "email", "outcome": "delivered"}); got != 1 {
		t.Errorf("delivered counter = %v, want 1", got)
	}
	if got := m.get("fhir_subs_channel_email_smtp_responses_total",
		map[string]string{"channel": "email", "class": "2xx"}); got != 1 {
		t.Errorf("smtp 2xx counter = %v, want 1", got)
	}
}

// TestIntegrationTransientThenPermanentDistinguished verifies the
// scheduler-facing distinction between 4xx (transient) and 5xx
// (permanent) is preserved end-to-end for both the RCPT and DATA
// stages.
func TestIntegrationTransientThenPermanentDistinguished(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  relayConfig
		want channel.OutcomeKind
		code int
	}{
		{
			name: "rcpt 4xx -> transient",
			cfg:  relayConfig{FailRcpt: &smtpResp{Code: 450, Message: "mailbox busy"}},
			want: channel.OutcomeTransient,
			code: 450,
		},
		{
			name: "rcpt 5xx -> permanent",
			cfg:  relayConfig{FailRcpt: &smtpResp{Code: 553, Message: "policy reject"}},
			want: channel.OutcomePermanent,
			code: 553,
		},
		{
			name: "data 4xx -> transient",
			cfg:  relayConfig{FailDataClose: &smtpResp{Code: 451, Message: "graylist"}},
			want: channel.OutcomeTransient,
			code: 451,
		},
		{
			name: "data 5xx -> permanent",
			cfg:  relayConfig{FailDataClose: &smtpResp{Code: 552, Message: "too large"}},
			want: channel.OutcomePermanent,
			code: 552,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := startTestRelay(t, tc.cfg)
			defer srv.Stop()

			ch := newChannel(t, email.Config{
				From:     "noreply@example.org",
				SMTPHost: srv.Host,
				SMTPPort: srv.Port,
				STARTTLS: email.STARTTLSDisabled,
			})
			env := newEnvelope("subscriber@example.org")
			out, _ := ch.Deliver(context.Background(), env)
			if out.Kind != tc.want {
				t.Fatalf("kind = %v, want %v (reason=%q)", out.Kind, tc.want, out.Reason)
			}
			if out.StatusCode != tc.code {
				t.Errorf("StatusCode = %d, want %d", out.StatusCode, tc.code)
			}
		})
	}
}
