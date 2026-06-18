// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestE2E_Email_STARTTLSRequired_RelayDoesNotAdvertise_PermanentFailure
// verifies B-14: the email channel defaults to STARTTLSRequired, and a
// relay that does not advertise STARTTLS causes a Permanent failure
// (no plaintext PHI fallback). End-to-end against a stub SMTP relay.
func TestE2E_Email_STARTTLSRequired_RelayDoesNotAdvertise_PermanentFailure(t *testing.T) {
	t.Parallel()

	relay := startNoSTARTTLSRelay(t)
	defer relay.stop()

	host, portStr, err := net.SplitHostPort(relay.addr())
	if err != nil {
		t.Fatalf("split relay addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	// Default config (no explicit STARTTLS) -> Required by default.
	ch, err := email.New(email.Config{
		From:           "noreply@example.org",
		SMTPHost:       host,
		SMTPPort:       port,
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}

	env := channel.NotificationEnvelope{
		SubscriptionID:       uuid.New(),
		Sequence:             1,
		BundleBytes:          []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`),
		BundleKind:           channel.BundleEventNotification,
		PayloadType:          channel.PayloadIDOnly,
		ContentType:          channel.ContentTypeFHIRJSON,
		CorrelationID:        uuid.NewString(),
		SubscriptionEndpoint: "mailto:rcpt@example.org",
		Deadline:             time.Now().Add(5 * time.Second),
	}
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomePermanent {
		t.Fatalf("expected Permanent (STARTTLS required by default); got %v: %q",
			out.Kind, out.Reason)
	}
	if !strings.Contains(strings.ToLower(out.Reason), "starttls") {
		t.Errorf("reason should mention starttls; got %q", out.Reason)
	}
}

// stubRelay is a minimal SMTP server that does not advertise STARTTLS.
// It exists only to give the email channel a place to send to.
type stubRelay struct {
	listener net.Listener
	wg       sync.WaitGroup
}

func startNoSTARTTLSRelay(t *testing.T) *stubRelay {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := &stubRelay{listener: l}
	r.wg.Add(1)
	go r.serve()
	return r
}

func (r *stubRelay) addr() string {
	return r.listener.Addr().String()
}

func (r *stubRelay) stop() {
	_ = r.listener.Close()
	r.wg.Wait()
}

func (r *stubRelay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.handle(c)
	}
}

func (r *stubRelay) handle(c net.Conn) {
	defer c.Close()
	w := bufio.NewWriter(c)
	br := bufio.NewReader(c)
	_, _ = w.WriteString("220 stub SMTP\r\n")
	_ = w.Flush()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		up := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			// Multi-line 250 response; do NOT advertise STARTTLS.
			_, _ = w.WriteString("250-stub greets you\r\n250 PIPELINING\r\n")
			_ = w.Flush()
		case strings.HasPrefix(up, "QUIT"):
			_, _ = w.WriteString("221 bye\r\n")
			_ = w.Flush()
			return
		default:
			_, _ = w.WriteString("502 not implemented\r\n")
			_ = w.Flush()
		}
	}
}
