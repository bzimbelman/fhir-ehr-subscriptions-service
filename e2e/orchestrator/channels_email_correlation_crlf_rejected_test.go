// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestE2E_Email_CorrelationID_CRLF_RejectedPermanent verifies B-16:
// a NotificationEnvelope whose CorrelationID contains CR/LF must
// produce a Permanent failure before any wire I/O. Pairs with the
// belt-and-braces stripCRLF in writeHeader.
func TestE2E_Email_CorrelationID_CRLF_RejectedPermanent(t *testing.T) {
	t.Parallel()

	relay := startNoSTARTTLSRelay(t)
	defer relay.stop()

	host, portStr, _ := splitHostPort(relay.addr())
	ch, err := email.New(email.Config{
		From:               "noreply@example.org",
		SMTPHost:           host,
		SMTPPort:           portStr,
		STARTTLS:           email.STARTTLSDisabled,
		AllowCleartextAuth: true,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}

	bad := []string{
		"abc\r\nBcc: attacker@example.org",
		"abc\nX-Forged: yes",
		"abc\rsmuggle",
	}
	for _, c := range bad {
		c := c
		t.Run(c, func(t *testing.T) {
			env := channel.NotificationEnvelope{
				SubscriptionID:       uuid.New(),
				Sequence:             1,
				BundleBytes:          []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`),
				BundleKind:           channel.BundleEventNotification,
				ContentType:          channel.ContentTypeFHIRJSON,
				CorrelationID:        c,
				SubscriptionEndpoint: "mailto:rcpt@example.org",
				Deadline:             time.Now().Add(2 * time.Second),
			}
			out, _ := ch.Deliver(context.Background(), env)
			if out.Kind != channel.OutcomePermanent {
				t.Fatalf("CRLF correlation id must be Permanent failure; got %v: %q",
					out.Kind, out.Reason)
			}
			if !strings.Contains(strings.ToLower(out.Reason), "correlation") {
				t.Errorf("reason should reference correlation id; got %q", out.Reason)
			}
		})
	}
}

// splitHostPort is a small helper that returns host + numeric port.
func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := splitHostPortRaw(addr)
	if err != nil {
		return "", 0, err
	}
	port := 0
	for i := 0; i < len(portStr); i++ {
		port = port*10 + int(portStr[i]-'0')
	}
	return host, port, nil
}

func splitHostPortRaw(addr string) (string, string, error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", &addrError{addr: addr, msg: "missing port"}
	}
	return addr[:i], addr[i+1:], nil
}

type addrError struct {
	addr string
	msg  string
}

func (e *addrError) Error() string { return "addr " + e.addr + ": " + e.msg }
