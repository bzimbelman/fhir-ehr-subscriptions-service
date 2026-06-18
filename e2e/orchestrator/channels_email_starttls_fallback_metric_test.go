// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestE2E_Email_STARTTLSPreferredFallback_EmitsMetric verifies S-6 #2:
// an operator running with STARTTLS=Preferred against a relay that does
// NOT advertise STARTTLS gets a compliance signal. The channel emits
// fhir_subs_channel_email_starttls_outcome_total{policy=preferred,
// upgraded=false} so the operator can alert on strip-STARTTLS / relay
// regression.
func TestE2E_Email_STARTTLSPreferredFallback_EmitsMetric(t *testing.T) {
	t.Parallel()

	relay := startE2EAcceptingRelay(t, false /* enable STARTTLS */)
	defer relay.stop()

	host, portStr, err := net.SplitHostPort(relay.addr())
	if err != nil {
		t.Fatalf("split relay addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	m := newE2EFakeChannelMetrics()
	ch, err := email.New(email.Config{
		From:           "noreply@example.org",
		SMTPHost:       host,
		SMTPPort:       port,
		STARTTLS:       email.STARTTLSPreferred,
		RequestTimeout: 5 * time.Second,
		Metrics:        m,
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
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered (Preferred falls back to plaintext); got %v: %q", out.Kind, out.Reason)
	}

	got := m.get(email.MetricSTARTTLSOutcomeTotal,
		map[string]string{"channel": "email", "policy": "preferred", "upgraded": "false"})
	if got != 1 {
		t.Fatalf("expected 1 starttls fallback metric, got %v", got)
	}
}

// e2eAcceptingRelay accepts SMTP connections and walks through HELO/MAIL/
// RCPT/DATA without TLS. It does NOT advertise STARTTLS. Just enough
// for an end-to-end Preferred-fallback verification.
type e2eAcceptingRelay struct {
	listener net.Listener
	wg       sync.WaitGroup
}

func startE2EAcceptingRelay(t *testing.T, enableSTARTTLS bool) *e2eAcceptingRelay {
	t.Helper()
	if enableSTARTTLS {
		t.Fatal("e2eAcceptingRelay does not implement STARTTLS — pass false")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	r := &e2eAcceptingRelay{listener: l}
	r.wg.Add(1)
	go r.serve()
	return r
}

func (r *e2eAcceptingRelay) addr() string {
	return r.listener.Addr().String()
}

func (r *e2eAcceptingRelay) stop() {
	_ = r.listener.Close()
	r.wg.Wait()
}

func (r *e2eAcceptingRelay) serve() {
	defer r.wg.Done()
	for {
		c, err := r.listener.Accept()
		if err != nil {
			return
		}
		go r.handle(c)
	}
}

// handle implements the smallest SMTP server that walks an email
// channel through DATA without TLS. Behavior matches the unit tests'
// in-package relay; we duplicate it here to keep e2e standalone.
func (r *e2eAcceptingRelay) handle(c net.Conn) {
	defer c.Close()
	send := func(s string) error {
		_, err := c.Write([]byte(s + "\r\n"))
		return err
	}
	if err := send("220 e2e-relay ESMTP"); err != nil {
		return
	}
	buf := make([]byte, 4096)
	leftover := []byte{}
	readLine := func() (string, error) {
		for {
			if i := indexOfNL(leftover); i >= 0 {
				line := string(leftover[:i])
				leftover = leftover[i+1:]
				return line, nil
			}
			n, err := c.Read(buf)
			if err != nil {
				return "", err
			}
			leftover = append(leftover, buf[:n]...)
		}
	}
	mailFrom := ""
	rcpt := []string{}
	for {
		line, err := readLine()
		if err != nil {
			return
		}
		line = trimCRLF(line)
		up := upperASCII(line)
		switch {
		case startsWith(up, "EHLO"), startsWith(up, "HELO"):
			_ = send("250-e2e-relay")
			_ = send("250 PIPELINING")
		case startsWith(up, "MAIL FROM:"):
			mailFrom = line[len("MAIL FROM:"):]
			_ = send("250 OK")
		case startsWith(up, "RCPT TO:"):
			rcpt = append(rcpt, line[len("RCPT TO:"):])
			_ = send("250 OK")
		case startsWith(up, "DATA"):
			_ = send("354 go ahead")
			// Read until line containing only ".".
			for {
				dl, derr := readLine()
				if derr != nil {
					return
				}
				if trimCRLF(dl) == "." {
					break
				}
			}
			_ = send("250 OK queued")
			_ = mailFrom
			_ = rcpt
			mailFrom = ""
			rcpt = nil
		case startsWith(up, "QUIT"):
			_ = send("221 bye")
			return
		default:
			_ = send("502 not implemented")
		}
	}
}

func indexOfNL(b []byte) int {
	for i, v := range b {
		if v == '\n' {
			return i
		}
	}
	return -1
}

func trimCRLF(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\r' || s[len(s)-1] == '\n') {
		s = s[:len(s)-1]
	}
	return s
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func upperASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// e2eFakeChannelMetrics is a tiny in-memory channel.MetricsEmitter for
// e2e assertions.
type e2eFakeChannelMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func newE2EFakeChannelMetrics() *e2eFakeChannelMetrics {
	return &e2eFakeChannelMetrics{counters: map[string]float64{}}
}

func (m *e2eFakeChannelMetrics) Inc(name string, labels map[string]string) {
	m.Add(name, 1, labels)
}

func (m *e2eFakeChannelMetrics) Add(name string, delta float64, labels map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[e2eMetricKey(name, labels)] += delta
}

func (m *e2eFakeChannelMetrics) Observe(string, float64, map[string]string) {}
func (m *e2eFakeChannelMetrics) Set(string, float64, map[string]string)     {}

func (m *e2eFakeChannelMetrics) get(name string, labels map[string]string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[e2eMetricKey(name, labels)]
}

func e2eMetricKey(name string, labels map[string]string) string {
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
	out := name
	for _, k := range keys {
		out += "|" + k + "=" + labels[k]
	}
	return out
}
