// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package websocket_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/channel/websocket"
)

// fakeMetrics records counter increments so tests can assert them.
type fakeMetrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{counters: map[string]float64{}}
}

func (f *fakeMetrics) Inc(name string, labels map[string]string) {
	f.Add(name, 1, labels)
}

func (f *fakeMetrics) Add(name string, delta float64, labels map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counters[mkKey(name, labels)] += delta
}

func (f *fakeMetrics) Observe(string, float64, map[string]string) {}
func (f *fakeMetrics) Set(string, float64, map[string]string)     {}

func (f *fakeMetrics) get(name string, labels map[string]string) float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[mkKey(name, labels)]
}

func mkKey(name string, labels map[string]string) string {
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

// fakeTokens is an in-memory websocket.TokenConsumer used to drive the
// bind handler without a database.
type fakeTokens struct {
	mu     sync.Mutex
	rows   map[string]*tokenRow
	now    func() time.Time
	calls  int
}

type tokenRow struct {
	subID    uuid.UUID
	clientID string
	expires  time.Time
	consumed bool
}

func newFakeTokens(now func() time.Time) *fakeTokens {
	return &fakeTokens{rows: map[string]*tokenRow{}, now: now}
}

func (f *fakeTokens) Mint(token string, subID uuid.UUID, clientID string, expiresAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[token] = &tokenRow{subID: subID, clientID: clientID, expires: expiresAt}
}

func (f *fakeTokens) Consume(_ context.Context, token string, now time.Time) (websocket.ConsumeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	row, ok := f.rows[token]
	if !ok {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeNotFound}, nil
	}
	if row.consumed {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeAlreadyUsed}, nil
	}
	if !row.expires.After(now) {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeExpired}, nil
	}
	row.consumed = true
	return websocket.ConsumeResult{
		Outcome:        websocket.ConsumeOK,
		SubscriptionID: row.subID,
		ClientID:       row.clientID,
	}, nil
}

// fakeReplayer is an in-memory websocket.EventReplayer.
type fakeReplayer struct {
	mu     sync.Mutex
	events map[uuid.UUID][]websocket.PastEvent
}

func newFakeReplayer() *fakeReplayer {
	return &fakeReplayer{events: map[uuid.UUID][]websocket.PastEvent{}}
}

func (f *fakeReplayer) Append(subID uuid.UUID, evs ...websocket.PastEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[subID] = append(f.events[subID], evs...)
}

func (f *fakeReplayer) ReplaySince(_ context.Context, subID uuid.UUID, after uint64) ([]websocket.PastEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := f.events[subID]
	out := make([]websocket.PastEvent, 0, len(in))
	for _, e := range in {
		if e.EventNumber > after {
			out = append(out, e)
		}
	}
	return out, nil
}

// helper builds an envelope.
func newEnvelope(subID uuid.UUID, seq uint64, body []byte) channel.NotificationEnvelope {
	return channel.NotificationEnvelope{
		SubscriptionID: subID,
		Sequence:       seq,
		BundleBytes:    body,
		BundleKind:     channel.BundleEventNotification,
		PayloadType:    channel.PayloadIDOnly,
		ContentType:    channel.ContentTypeFHIRJSON,
		Attempt:        0,
		CorrelationID:  uuid.New().String(),
		Deadline:       time.Now().Add(5 * time.Second),
	}
}

// dialClient performs the WSS upgrade against the test http server.
func dialClient(t *testing.T, srv *httptest.Server) *codingws.Conn {
	t.Helper()
	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := codingws.Dial(ctx, u, &codingws.DialOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func writeBind(t *testing.T, c *codingws.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := c.Write(ctx, codingws.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
}

func readText(t *testing.T, c *codingws.Conn, dur time.Duration) (codingws.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	mt, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return mt, data
}

// --- Tests ---

func TestNewRejectsMissingDeps(t *testing.T) {
	t.Parallel()

	_, err := websocket.New(websocket.Options{})
	if err == nil {
		t.Fatalf("expected error when Tokens is nil")
	}
}

func TestBindWithValidTokenSucceeds(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("good-token", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
		Metrics:  newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")

	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"good-token"}`)

	mt, data := readText(t, conn, 2*time.Second)
	if mt != codingws.MessageText {
		t.Fatalf("type = %v", mt)
	}
	if !strings.Contains(string(data), `"bind-success"`) {
		t.Errorf("bind reply = %s", data)
	}
}

func TestBindWithConsumedTokenIsRejected(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("once", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	// First bind succeeds and the connection is held.
	c1 := dialClient(t, srv)
	writeBind(t, c1, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"once"}`)
	if _, data := readText(t, c1, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("first bind = %s", data)
	}

	// Second bind with the same token must fail closed.
	c2 := dialClient(t, srv)
	defer c2.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c2, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"once"}`)
	mt, data := readText(t, c2, 2*time.Second)
	_ = mt
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Errorf("second bind = %s", data)
	}

	// Cleanly close c1.
	_ = c1.Close(codingws.StatusNormalClosure, "")
}

func TestBindWithExpiredTokenIsRejected(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("stale", subID, "client-a", now().Add(-1*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")

	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"stale"}`)
	_, data := readText(t, conn, 2*time.Second)
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Errorf("expected bind-error, got %s", data)
	}
}

func TestBindWithMismatchedSubscriptionIsRejected(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	bound := uuid.New()
	other := uuid.New()
	tokens.Mint("tok", bound, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")

	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+other.String()+`","token":"tok"}`)
	_, data := readText(t, conn, 2*time.Second)
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Errorf("expected bind-error for mismatched sub, got %s", data)
	}
}

func TestDeliverWithoutBindReturnsTransient(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Now() }
	ch, err := websocket.New(websocket.Options{
		Tokens:              newFakeTokens(now),
		Replayer:            newFakeReplayer(),
		Now:                 now,
		TransientRetryAfter: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer ch.Close()

	out, err := ch.Deliver(context.Background(), newEnvelope(uuid.New(), 1, []byte(`{}`)))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomeTransient {
		t.Errorf("expected transient when not bound, got %v", out.Kind)
	}
	if out.RetryAfter != 5*time.Second {
		t.Errorf("retry-after = %v", out.RetryAfter)
	}
}

func TestDeliverFrameTooLargeIsPermanent(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("t", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:        tokens,
		Replayer:      newFakeReplayer(),
		Now:           now,
		MaxFrameBytes: 16,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"t"}`)
	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	out, err := ch.Deliver(context.Background(), newEnvelope(subID, 1, []byte(strings.Repeat("x", 64))))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if out.Kind != channel.OutcomePermanent {
		t.Errorf("expected permanent for oversize frame, got %v: %q", out.Kind, out.Reason)
	}
}

func TestDeliverDeliversAndAck(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
		Metrics:  newFakeMetrics(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok"}`)
	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	body := []byte(`{"resourceType":"Bundle","type":"subscription-notification"}`)

	// Drive Deliver in parallel with the client read so the channel can flush
	// the frame on its session goroutine.
	done := make(chan channel.DeliveryOutcome, 1)
	go func() {
		out, _ := ch.Deliver(context.Background(), newEnvelope(subID, 7, body))
		done <- out
	}()

	mt, data := readText(t, conn, 2*time.Second)
	if mt != codingws.MessageText {
		t.Fatalf("frame type = %v", mt)
	}
	if string(data) != string(body) {
		t.Errorf("frame body = %s", data)
	}

	// Subscriber acks the event.
	writeBind(t, conn, `{"type":"ack","eventNumber":7}`)

	out := <-done
	if out.Kind != channel.OutcomeDelivered {
		t.Errorf("outcome = %v %q", out.Kind, out.Reason)
	}
}

func TestReplayOnReconnectSendsMissedEvents(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("first", subID, "client-a", now().Add(60*time.Second))
	tokens.Mint("second", subID, "client-a", now().Add(60*time.Second))

	replayer := newFakeReplayer()
	replayer.Append(subID,
		websocket.PastEvent{EventNumber: 5, Bundle: []byte(`{"e":5}`), ContentType: channel.ContentTypeFHIRJSON},
		websocket.PastEvent{EventNumber: 6, Bundle: []byte(`{"e":6}`), ContentType: channel.ContentTypeFHIRJSON},
	)

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: replayer,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	// Subscriber reconnects with last-known event 4; expects 5 then 6.
	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")

	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"first","lastReceivedEventNumber":4}`)

	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}
	_, e1 := readText(t, conn, 2*time.Second)
	if !strings.Contains(string(e1), `"e":5`) {
		t.Errorf("first replay = %s", e1)
	}
	_, e2 := readText(t, conn, 2*time.Second)
	if !strings.Contains(string(e2), `"e":6`) {
		t.Errorf("second replay = %s", e2)
	}
}

func TestSessionStaysBoundAfterIdle(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens: tokens,
		// Pings disabled so the test does not race with control-frame
		// timing; this test focuses on idle session retention, not on
		// proving the ping loop runs (covered indirectly by integration
		// tests that exercise the real wire).
		Replayer:     newFakeReplayer(),
		Now:          now,
		PingInterval: time.Hour,
		IdleTimeout:  time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok"}`)
	if _, data := readText(t, conn, 1*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	// After idle time, the channel should still consider the subscription
	// bound. A subsequent Deliver must reach the subscriber, not return
	// "no socket".
	time.Sleep(150 * time.Millisecond)

	body := []byte(`{"e":"after-idle"}`)
	done := make(chan channel.DeliveryOutcome, 1)
	go func() {
		out, _ := ch.Deliver(context.Background(), newEnvelope(subID, 11, body))
		done <- out
	}()

	_, frame := readText(t, conn, 2*time.Second)
	if string(frame) != string(body) {
		t.Errorf("frame after idle = %s", frame)
	}
	writeBind(t, conn, `{"type":"ack","eventNumber":11}`)
	if out := <-done; out.Kind != channel.OutcomeDelivered {
		t.Errorf("post-idle deliver = %v %q", out.Kind, out.Reason)
	}
}

func TestUnknownMessageTypeReturnsError(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Now() }
	tokens := newFakeTokens(now)

	ch, err := websocket.New(websocket.Options{
		Tokens:   tokens,
		Replayer: newFakeReplayer(),
		Now:      now,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeBind(t, conn, `{"type":"hello"}`)

	mt, data := readText(t, conn, 2*time.Second)
	if mt != codingws.MessageText {
		t.Fatalf("type = %v", mt)
	}
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Errorf("expected bind-error for unknown msg, got %s", data)
	}
}

// errors.Is sanity check (kept simple; future failure-mode mapping evolves).
var _ = errors.New
