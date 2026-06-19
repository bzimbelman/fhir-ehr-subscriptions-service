// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	chemail "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// Phase A — RED tests for OP #114 (email scope reduction): the
// production wiring MUST register a real RCPT-TO probe activator under
// the "email" channels map key, NOT defaultActivator{} (which always
// returns HandshakeSucceeded and lies about activation).
//
// These tests fail today because:
//   - newEmailActivator does not exist.
//   - cmd/fhir-subs/wiring.go still registers defaultActivator{} for
//     "email" (verified by the "no defaultActivator for email" test
//     that lives next to the existing wiring_channels_test.go suite).

// TestEmailActivator_RealRelayAcceptsRCPT drives the activator against a
// minimal SMTP relay that accepts EHLO/MAIL FROM/RCPT TO and asserts
// that the activator returns HandshakeSucceeded — i.e., the real relay
// classified the recipient as deliverable. The test runs the full SMTP
// path (no fakes / no mocks) by spinning up a tiny in-process listener.
func TestEmailActivator_RealRelayAcceptsRCPT(t *testing.T) {
	t.Parallel()

	relay := startProbeRelay(t, probeRelayConfig{})
	defer relay.Stop()

	ch, err := chemail.New(chemail.Config{
		From:     "noreply@example.org",
		SMTPHost: relay.Host,
		SMTPPort: relay.Port,
		STARTTLS: chemail.STARTTLSDisabled,
	})
	if err != nil {
		t.Fatalf("chemail.New: %v", err)
	}

	act := newEmailActivator(ch)

	row := repos.SubscriptionRow{
		ID:          uuid.New(),
		ChannelType: "email",
		Endpoint:    "mailto:subscriber@example.org",
	}

	outcome, aErr := act.ActivateSubscription(context.Background(), row)
	if aErr != nil {
		t.Fatalf("ActivateSubscription: %v", aErr)
	}
	if outcome != handlers.HandshakeSucceeded {
		t.Fatalf("outcome = %q; want %q", outcome, handlers.HandshakeSucceeded)
	}
	// The probe must reach RCPT TO but never DATA.
	if relay.RcptCount() == 0 {
		t.Errorf("relay never saw RCPT TO; activator did not exercise the real SMTP path")
	}
	if relay.DataCount() != 0 {
		t.Errorf("relay saw DATA from activator; RCPT-TO probe must NOT submit a body")
	}
}

// TestEmailActivator_RealRelayRejectsRCPT drives the activator against
// a relay that returns 5xx on RCPT TO and asserts HandshakeFailed.
func TestEmailActivator_RealRelayRejectsRCPT(t *testing.T) {
	t.Parallel()

	relay := startProbeRelay(t, probeRelayConfig{
		FailRcpt: &probeRelayResp{Code: 550, Message: "No such user"},
	})
	defer relay.Stop()

	ch, err := chemail.New(chemail.Config{
		From:     "noreply@example.org",
		SMTPHost: relay.Host,
		SMTPPort: relay.Port,
		STARTTLS: chemail.STARTTLSDisabled,
	})
	if err != nil {
		t.Fatalf("chemail.New: %v", err)
	}

	act := newEmailActivator(ch)

	row := repos.SubscriptionRow{
		ID:          uuid.New(),
		ChannelType: "email",
		Endpoint:    "mailto:nobody@example.org",
	}
	outcome, aErr := act.ActivateSubscription(context.Background(), row)
	if aErr != nil {
		t.Fatalf("ActivateSubscription: %v", aErr)
	}
	if outcome != handlers.HandshakeFailed {
		t.Fatalf("outcome = %q; want %q (5xx RCPT must not flip to active)", outcome, handlers.HandshakeFailed)
	}
}

// TestEmailActivator_DialFailureTransient asserts that a closed relay
// port classifies as HandshakeFailed (transient at the SMTP layer; the
// API translates Failed -> SubError so the row does not stay at
// "requested").
func TestEmailActivator_DialFailureTransient(t *testing.T) {
	t.Parallel()

	ch, err := chemail.New(chemail.Config{
		From:           "noreply@example.org",
		SMTPHost:       "127.0.0.1",
		SMTPPort:       1, // closed port
		STARTTLS:       chemail.STARTTLSDisabled,
		RequestTimeout: 1 * time.Second,
	})
	if err != nil {
		t.Fatalf("chemail.New: %v", err)
	}

	act := newEmailActivator(ch)

	row := repos.SubscriptionRow{
		ID:          uuid.New(),
		ChannelType: "email",
		Endpoint:    "mailto:subscriber@example.org",
	}
	outcome, aErr := act.ActivateSubscription(context.Background(), row)
	if aErr != nil {
		t.Fatalf("ActivateSubscription: %v", aErr)
	}
	if outcome != handlers.HandshakeFailed {
		t.Errorf("outcome = %q; want %q on dial failure", outcome, handlers.HandshakeFailed)
	}
}

// TestEmailActivator_InvalidEndpointFails asserts a non-mailto endpoint
// fails the handshake without making any network call.
func TestEmailActivator_InvalidEndpointFails(t *testing.T) {
	t.Parallel()

	relay := startProbeRelay(t, probeRelayConfig{})
	defer relay.Stop()

	ch, err := chemail.New(chemail.Config{
		From:     "noreply@example.org",
		SMTPHost: relay.Host,
		SMTPPort: relay.Port,
		STARTTLS: chemail.STARTTLSDisabled,
	})
	if err != nil {
		t.Fatalf("chemail.New: %v", err)
	}

	act := newEmailActivator(ch)
	row := repos.SubscriptionRow{
		ID:          uuid.New(),
		ChannelType: "email",
		Endpoint:    "https://not-a-mailto.example/",
	}

	outcome, aErr := act.ActivateSubscription(context.Background(), row)
	if aErr != nil {
		t.Fatalf("ActivateSubscription: %v", aErr)
	}
	if outcome != handlers.HandshakeFailed {
		t.Errorf("outcome = %q; want %q for non-mailto endpoint", outcome, handlers.HandshakeFailed)
	}
	if relay.ConnectCount() != 0 {
		t.Errorf("relay saw %d connections; activator must not dial when endpoint is malformed", relay.ConnectCount())
	}
}

// --- Probe relay ----------------------------------------------------------
//
// Tiny in-process SMTP relay scoped to the activator wiring tests. We do
// not depend on the email package's testRelay type because tests in
// other packages cannot import _test files. The relay supports just
// enough of RFC 5321 to drive the probe: greet, EHLO, MAIL FROM, RCPT
// TO, RSET, QUIT, and (when configured) DATA accounting so we can assert
// the activator never submits a body.

type probeRelayResp struct {
	Code    int
	Message string
}

type probeRelayConfig struct {
	// FailRcpt, when non-nil, returns this code on RCPT TO instead of 250.
	FailRcpt *probeRelayResp
}

type probeRelay struct {
	t        *testing.T
	cfg      probeRelayConfig
	listener net.Listener
	Host     string
	Port     int

	mu       sync.Mutex
	rcpt     int
	data     int
	conns    int
	stopped  chan struct{}
	stopFlag bool
}

func startProbeRelay(t *testing.T, cfg probeRelayConfig) *probeRelay {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tcp := l.Addr().(*net.TCPAddr)
	r := &probeRelay{
		t:        t,
		cfg:      cfg,
		listener: l,
		Host:     "127.0.0.1",
		Port:     tcp.Port,
		stopped:  make(chan struct{}),
	}
	go r.serve()
	return r
}

func (r *probeRelay) Stop() {
	r.mu.Lock()
	r.stopFlag = true
	r.mu.Unlock()
	_ = r.listener.Close()
	<-r.stopped
}

func (r *probeRelay) RcptCount() int    { r.mu.Lock(); defer r.mu.Unlock(); return r.rcpt }
func (r *probeRelay) DataCount() int    { r.mu.Lock(); defer r.mu.Unlock(); return r.data }
func (r *probeRelay) ConnectCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.conns }

func (r *probeRelay) serve() {
	defer close(r.stopped)
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		r.mu.Lock()
		r.conns++
		r.mu.Unlock()
		go r.handle(conn)
	}
}

func (r *probeRelay) handle(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	wr := bufio.NewWriter(c)
	flush := func(s string) bool {
		if _, err := wr.WriteString(s + "\r\n"); err != nil {
			return false
		}
		return wr.Flush() == nil
	}
	if !flush("220 probe-relay ESMTP") {
		return
	}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			// Single-line greeting; no extensions advertised so the
			// channel will not attempt STARTTLS / AUTH.
			if !flush("250 probe-relay") {
				return
			}
		case strings.HasPrefix(upper, "MAIL FROM:"):
			if !flush("250 OK") {
				return
			}
		case strings.HasPrefix(upper, "RCPT TO:"):
			r.mu.Lock()
			r.rcpt++
			r.mu.Unlock()
			if r.cfg.FailRcpt != nil {
				resp := r.cfg.FailRcpt
				if !flush(fmt.Sprintf("%d %s", resp.Code, resp.Message)) {
					return
				}
				continue
			}
			if !flush("250 OK") {
				return
			}
		case strings.HasPrefix(upper, "RSET"):
			if !flush("250 OK") {
				return
			}
		case strings.HasPrefix(upper, "DATA"):
			r.mu.Lock()
			r.data++
			r.mu.Unlock()
			if !flush("354 send body") {
				return
			}
			// Drain until terminator; any DATA invocation is itself a
			// test failure (the probe must not get here).
			for {
				body, berr := rd.ReadString('\n')
				if berr != nil {
					return
				}
				if strings.TrimRight(body, "\r\n") == "." {
					break
				}
			}
			if !flush("250 OK") {
				return
			}
		case strings.HasPrefix(upper, "QUIT"):
			_ = flush("221 Bye")
			return
		case strings.HasPrefix(upper, "NOOP"):
			if !flush("250 OK") {
				return
			}
		default:
			if !flush("502 unrecognized command") {
				return
			}
		}
	}
}
