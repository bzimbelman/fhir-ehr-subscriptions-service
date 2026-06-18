// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// B-14: Default STARTTLS policy must be Required (not Preferred).
//
// Healthcare service shipping PHI in cleartext over the public internet
// is a HIPAA breach. Operators must opt into the weaker Preferred policy
// explicitly.
func TestNewDefaultsToSTARTTLSRequired(t *testing.T) {
	t.Parallel()

	srv := startTestRelay(t, relayConfig{EnableSTARTTLS: false})
	defer srv.Stop()

	// Configure WITHOUT setting STARTTLS — should default to Required.
	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
	})

	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)

	// Default Required + relay does not advertise STARTTLS -> Permanent failure.
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent (STARTTLS required by default), got %v: %q",
			out.Kind, out.Reason)
	}
	if !strings.Contains(strings.ToLower(out.Reason), "starttls") {
		t.Errorf("reason should mention starttls; got %q", out.Reason)
	}
}

// B-15: Refuse to construct the channel when AUTH is configured to ship
// credentials in plaintext (STARTTLS=Disabled with a non-empty
// AuthMechanism), unless the operator explicitly sets AllowCleartextAuth.
func TestNewRefusesCleartextAuth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mech     email.AuthMechanism
		policy   email.STARTTLSPolicy
		expectOK bool
	}{
		{"plain over disabled refused", email.AuthPlain, email.STARTTLSDisabled, false},
		{"login over disabled refused", email.AuthLogin, email.STARTTLSDisabled, false},
		{"cram-md5 over disabled refused", email.AuthCRAMMD5, email.STARTTLSDisabled, false},
		// AuthNone with disabled is fine — no creds to leak.
		{"no auth over disabled ok", email.AuthNone, email.STARTTLSDisabled, true},
		// Required is fine: connection always TLS.
		{"plain over required ok", email.AuthPlain, email.STARTTLSRequired, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := email.Config{
				From:          "noreply@example.org",
				SMTPHost:      "smtp.example.org",
				SMTPPort:      25,
				STARTTLS:      tc.policy,
				AuthMechanism: tc.mech,
				AuthUsername:  "u",
				AuthPassword:  "p",
			}
			_, err := email.New(cfg)
			if tc.expectOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.expectOK && err == nil {
				t.Fatalf("expected error refusing cleartext AUTH")
			}
		})
	}
}

// B-15: Operator must be able to opt into cleartext AUTH explicitly via
// AllowCleartextAuth (intended for a closed local relay).
func TestNewAllowsCleartextAuthWhenExplicitlyOptedIn(t *testing.T) {
	t.Parallel()
	cfg := email.Config{
		From:               "noreply@example.org",
		SMTPHost:           "smtp.example.org",
		SMTPPort:           25,
		STARTTLS:           email.STARTTLSDisabled,
		AuthMechanism:      email.AuthPlain,
		AuthUsername:       "u",
		AuthPassword:       "p",
		AllowCleartextAuth: true,
	}
	if _, err := email.New(cfg); err != nil {
		t.Fatalf("expected New to succeed with AllowCleartextAuth=true; got %v", err)
	}
}

// B-16: Reject CRLF in CorrelationID. CRLF in a header value forges
// arbitrary SMTP headers and can smuggle a message body. The channel
// must refuse to build the MIME message and return PermanentFailure.
func TestDeliverRejectsCRLFInCorrelationID(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:               "noreply@example.org",
		SMTPHost:           srv.Host,
		SMTPPort:           srv.Port,
		STARTTLS:           email.STARTTLSDisabled,
		AllowCleartextAuth: true,
	})
	bad := []string{
		"abc\r\nBcc: attacker@example.org",
		"abc\nBcc: attacker@example.org",
		"abc\rwhatever",
	}
	for _, c := range bad {
		c := c
		t.Run(c, func(t *testing.T) {
			env := newEnvelope("subscriber@example.org")
			env.CorrelationID = c
			out, _ := ch.Deliver(context.Background(), env)
			if out.Kind != channel.OutcomePermanent {
				t.Fatalf("expected Permanent for CRLF correlation id; got %v: %q",
					out.Kind, out.Reason)
			}
			// And nothing must be written to the relay.
			if got := len(srv.Received()); got != 0 {
				t.Errorf("relay received %d messages; want 0 (delivery should abort before SMTP)", got)
			}
		})
	}
}

// B-16: A correlation ID containing CRLF must NOT make it into the
// outbound MIME body even when the bundle path or Message-ID would
// normally accept it.
func TestDeliverRejectsCRLFInCorrelationIDViaMessageID(t *testing.T) {
	t.Parallel()
	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:               "noreply@example.org",
		SMTPHost:           srv.Host,
		SMTPPort:           srv.Port,
		STARTTLS:           email.STARTTLSDisabled,
		AllowCleartextAuth: true,
	})
	env := newEnvelope("subscriber@example.org")
	env.CorrelationID = "deadbeef\r\nX-Forged: yes"
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent; got %v: %q", out.Kind, out.Reason)
	}
	if got := len(srv.Received()); got != 0 {
		t.Errorf("relay received %d messages; want 0", got)
	}
	// Sanity: a fresh UUID still works (regression guard for the validator).
	envOK := newEnvelope("subscriber@example.org")
	envOK.CorrelationID = uuid.NewString()
	outOK, _ := ch.Deliver(context.Background(), envOK)
	if outOK.Kind != channel.OutcomeDelivered {
		t.Fatalf("normal correlation id should still deliver; got %v: %q",
			outOK.Kind, outOK.Reason)
	}
}
