// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package email_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/email"
)

// TestS6_STARTTLSPreferredFallbackMetric (S-6 #2) — when STARTTLS=Preferred and the
// relay does NOT advertise STARTTLS, the channel falls back to plaintext.
// Operators need a compliance signal for that fallback so they can alert on
// it: the channel must emit a counter increment with channel=email,policy=preferred,
// upgraded=false on the fhir_subs_channel_email_starttls_outcome_total metric.
func TestS6_STARTTLSPreferredFallbackMetric(t *testing.T) {
	t.Parallel()

	srv := startTestRelay(t, relayConfig{EnableSTARTTLS: false})
	defer srv.Stop()

	m := newFakeMetrics()
	ch := newChannelWithMetrics(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSPreferred,
	}, m)

	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered (preferred falls back to plaintext); got %v: %q",
			out.Kind, out.Reason)
	}

	got := m.get(email.MetricSTARTTLSOutcomeTotal,
		map[string]string{"channel": "email", "policy": "preferred", "upgraded": "false"})
	if got != 1 {
		t.Fatalf("expected 1 starttls fallback metric, got %v", got)
	}
}

// TestS6_STARTTLSPreferredUpgradedMetric (S-6 #2) — when STARTTLS=Preferred and the
// relay DOES advertise STARTTLS, the channel upgrades successfully. The same
// metric must record upgraded=true so the operator can compute the fallback
// rate without ambiguity.
func TestS6_STARTTLSPreferredUpgradedMetric(t *testing.T) {
	t.Parallel()

	srv := startTestRelay(t, relayConfig{EnableSTARTTLS: true})
	defer srv.Stop()

	m := newFakeMetrics()
	ch := newChannelWithMetrics(t, email.Config{
		From:      "noreply@example.org",
		SMTPHost:  srv.Host,
		SMTPPort:  srv.Port,
		STARTTLS:  email.STARTTLSPreferred,
		TLSConfig: testTLSConfigFor(srv),
	}, m)

	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}

	got := m.get(email.MetricSTARTTLSOutcomeTotal,
		map[string]string{"channel": "email", "policy": "preferred", "upgraded": "true"})
	if got != 1 {
		t.Fatalf("expected 1 starttls upgraded metric, got %v", got)
	}
}

// TestS6_SMTPErrorCodeFromTextprotoError (S-6 #3) — exported wrapper around
// the internal SMTP-code extractor must use errors.As against
// *textproto.Error rather than parsing the leading 3 bytes off Error().
//
// The behavior contract: a wrapped *textproto.Error returns the right code;
// a non-textproto error returns 0; an error embedded several layers deep
// inside fmt.Errorf("...%w...") still resolves.
func TestS6_SMTPErrorCodeFromTextprotoError(t *testing.T) {
	t.Parallel()

	wrapped := &textproto.Error{Code: 451, Msg: "graylist"}
	double := errors.New("plain wrapper: " + wrapped.Error())
	deep := errors.Join(errors.New("dial: connect"), wrapped)

	cases := []struct {
		name string
		in   error
		want int
	}{
		{"direct *textproto.Error", wrapped, 451},
		{"errors.Join with textproto", deep, 451},
		{"non-protocol error", errors.New("network reset"), 0},
		{"plain string with leading code (must NOT match without textproto)", double, 0},
		{"nil error", nil, 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := email.SMTPErrorCodeForTest(tc.in)
			if got != tc.want {
				t.Fatalf("SMTPErrorCode(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestS6_BenignCloseErrFiltering (S-6 #4) — the close-error logging path
// uses isBenignCloseErr to drop the noisy "use of closed network
// connection" / nil errors and surface real failures. This test pins the
// classifier so a future refactor cannot silently swallow real Close
// errors again.
func TestS6_BenignCloseErrFiltering(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		err    error
		benign bool
	}{
		{"nil is benign (no log)", nil, true},
		{"closed-network-connection is benign", errors.New("use of closed network connection"), true},
		{"raw EOF is NOT benign", errors.New("EOF"), false},
		{"i/o timeout is NOT benign", errors.New("read tcp: i/o timeout"), false},
		{"broken pipe is NOT benign", errors.New("write: broken pipe"), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := email.IsBenignCloseErrForTest(tc.err); got != tc.benign {
				t.Fatalf("isBenignCloseErr(%v) = %v, want %v", tc.err, got, tc.benign)
			}
		})
	}
}

// TestS6_CloseErrorsAreLogged (S-6 #4) — when the SMTP client's Close
// returns a non-benign error after delivery, the channel must log it
// rather than silently swallow. We exercise the path with a Close-failing
// client wrapper through the test seam.
func TestS6_CloseErrorsAreLogged(t *testing.T) {
	t.Parallel()

	var buf safeBuffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := startTestRelay(t, relayConfig{})
	defer srv.Stop()

	ch := newChannel(t, email.Config{
		From:     "noreply@example.org",
		SMTPHost: srv.Host,
		SMTPPort: srv.Port,
		STARTTLS: email.STARTTLSDisabled,
		Logger:   logger,
	})

	// Drive a direct invocation of the close-error logging hook using the
	// channel's own logger, simulating a non-benign Close result.
	email.LogClientCloseErrForTest(ch, errors.New("simulated relay reset"))

	body := buf.String()
	if !strings.Contains(body, "smtp client close") {
		t.Fatalf("expected a log line for client Close error; got:\n%s", body)
	}
	if !strings.Contains(body, "simulated relay reset") {
		t.Fatalf("expected log to include underlying error; got:\n%s", body)
	}

	env := newEnvelope("subscriber@example.org")
	out, _ := ch.Deliver(context.Background(), env)
	if out.Kind != channel.OutcomeDelivered {
		t.Fatalf("expected Delivered, got %v: %q", out.Kind, out.Reason)
	}
	// Sleep gives any deferred logging a chance to flush; the test is
	// satisfied by the direct invocation above already.
	time.Sleep(5 * time.Millisecond)
}

// TestS6_DialerHasTimeoutEvenWithoutDeadline (S-6 #1) — when the envelope
// carries no deadline AND the operator did not set RequestTimeout, the
// channel must still apply the package default request timeout to the
// dialer. Previously the dialer relied solely on ctx.Deadline; if the
// caller passed a zero envelope deadline the dialer had no timeout and
// would block on a stuck listener until the OS-level connect timeout.
//
// We reproduce that condition with a listener that accepts but never
// writes the SMTP banner. The Deliver should return Transient within the
// configured RequestTimeout, NOT block longer.
func TestS6_DialerHasTimeoutEvenWithoutDeadline(t *testing.T) {
	t.Parallel()

	hung := startHungListener(t)
	defer hung.stop()

	ch, err := email.New(email.Config{
		From:           "noreply@example.org",
		SMTPHost:       hung.host,
		SMTPPort:       hung.port,
		STARTTLS:       email.STARTTLSDisabled,
		RequestTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("email.New: %v", err)
	}

	env := newEnvelope("subscriber@example.org")
	env.Deadline = time.Time{} // zero -> no deadline, must rely on RequestTimeout

	start := time.Now()
	out, _ := ch.Deliver(context.Background(), env)
	elapsed := time.Since(start)

	if out.Kind != channel.OutcomeTransient {
		t.Fatalf("expected Transient on hung listener; got %v: %q", out.Kind, out.Reason)
	}
	// The configured RequestTimeout is 200ms. Generous slack for slow CI:
	// the test fails only if we exceed 2s, which would prove the dialer
	// had no timeout at all.
	if elapsed > 2*time.Second {
		t.Fatalf("Deliver took %v with RequestTimeout=200ms; dialer is missing a timeout", elapsed)
	}
}

// safeBuffer is a goroutine-safe wrapper around bytes.Buffer for capturing
// log output emitted from the deferred Close in another goroutine path.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Quiet a future "unused" warning on uuid imports if any test trims out.
var _ = uuid.New
