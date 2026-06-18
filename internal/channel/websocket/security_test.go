// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// B-17: WebSocket upgrade must enforce Origin checking. The default
// behavior (no OriginPatterns configured) is to deny cross-origin
// upgrades. Same-origin (httptest's automatic Origin) and explicitly
// allow-listed origins succeed.

// TestUpgradeRejectsCrossOriginByDefault verifies a connection that
// presents a foreign Origin header is rejected at the HTTP upgrade
// layer (HTTP 403) when no OriginPatterns is configured.
func TestUpgradeRejectsCrossOriginByDefault(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
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

	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	hdr := http.Header{}
	hdr.Set("Origin", "https://attacker.example")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, resp, err := codingws.Dial(ctx, u, &codingws.DialOptions{
		HTTPClient: srv.Client(),
		HTTPHeader: hdr,
	})
	if err == nil {
		_ = conn.Close(codingws.StatusNormalClosure, "")
		t.Fatalf("expected dial to fail; cross-origin upgrade succeeded")
	}
	if resp == nil {
		t.Fatalf("no response on rejected upgrade: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d; want 403", resp.StatusCode)
	}
}

// TestUpgradeAllowsConfiguredOrigin verifies that an Origin matching
// the configured pattern is accepted.
func TestUpgradeAllowsConfiguredOrigin(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("ok-token", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:         tokens,
		Replayer:       newFakeReplayer(),
		Now:            now,
		OriginPatterns: []string{"trusted.example"},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	hdr := http.Header{}
	hdr.Set("Origin", "https://trusted.example")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := codingws.Dial(ctx, u, &codingws.DialOptions{
		HTTPClient: srv.Client(),
		HTTPHeader: hdr,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(codingws.StatusNormalClosure, "")

	if err := conn.Write(ctx, codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ok-token"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"bind-success"`) {
		t.Errorf("bind reply = %s; want bind-success", data)
	}
}

// TestUpgradeRejectsUnlistedOriginWhenPatternsConfigured verifies that
// once OriginPatterns is set, a non-matching Origin is rejected.
func TestUpgradeRejectsUnlistedOriginWhenPatternsConfigured(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	ch, err := websocket.New(websocket.Options{
		Tokens:         tokens,
		Replayer:       newFakeReplayer(),
		Now:            now,
		OriginPatterns: []string{"trusted.example"},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	hdr := http.Header{}
	hdr.Set("Origin", "https://attacker.example")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, resp, err := codingws.Dial(ctx, u, &codingws.DialOptions{
		HTTPClient: srv.Client(),
		HTTPHeader: hdr,
	})
	if err == nil {
		_ = conn.Close(codingws.StatusNormalClosure, "")
		t.Fatalf("expected dial to fail; attacker origin succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403; got %v", resp)
	}
}

// B-18: Concurrent ack-arrival + delivery-timeout must not panic by
// closing an already-closed channel. The race is between Deliver's
// `defer cancelAck` and an ack being processed by deliverAck. We can't
// poke the unexported method, but we can drive the public surface with
// a tight ack/deadline collision via the public deliver+client-ack path.
func TestAckRaceDoesNotPanic(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("ack-race-token", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:     tokens,
		Replayer:   newFakeReplayer(),
		Now:        now,
		AckTimeout: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	conn, _, err := codingws.Dial(dctx, u, &codingws.DialOptions{HTTPClient: srv.Client()})
	dcancel()
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(codingws.StatusNormalClosure, "")

	wctx, wcancel := context.WithTimeout(context.Background(), 1*time.Second)
	if err := conn.Write(wctx, codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ack-race-token"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	wcancel()
	rctx, rcancel := context.WithTimeout(context.Background(), 1*time.Second)
	if _, _, err := conn.Read(rctx); err != nil {
		t.Fatalf("read bind: %v", err)
	}
	rcancel()

	// Race the ack against the delivery timeout. The client races the
	// deadline by sending an ack that may arrive before, during, or after
	// the deliver-side deadline elapses. We run many iterations to widen
	// the window in which the race manifests.
	const N = 200
	var wg sync.WaitGroup
	for i := 1; i <= N; i++ {
		// Client side: read the inbound notification, then send an ack
		// straight away (ack races the AckTimeout the server set).
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, _, _ = conn.Read(ctx)
		}()
		seq := uint64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := newEnvelope(subID, seq, []byte(`{"r":"x"}`))
			env.Deadline = time.Now().Add(60 * time.Millisecond)
			_, _ = ch.Deliver(context.Background(), env)
		}()
		// Send a few stray acks to amplify the race window — these
		// target the same sequence and must NOT close an already-closed
		// channel.
		go func() {
			ackCtx, ackCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer ackCancel()
			for j := 0; j < 3; j++ {
				_ = conn.Write(ackCtx, codingws.MessageText,
					[]byte(`{"type":"ack","eventNumber":`+itoa(seq)+`}`))
			}
		}()
	}
	wg.Wait()
}

// B-18: A direct race on cancelAck and deliverAck on the same sequence
// must not panic. We exercise it by bombarding a freshly-bound session
// with concurrent acks while delivers race their own cancellations.
func TestConcurrentAckCancelDoesNotCloseClosedChannel(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("ack-cancel", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:     tokens,
		Replayer:   newFakeReplayer(),
		Now:        now,
		AckTimeout: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"
	conn, _, err := codingws.Dial(context.Background(), u, &codingws.DialOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(codingws.StatusNormalClosure, "")

	if err := conn.Write(context.Background(), codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ack-cancel"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	if _, _, err := conn.Read(context.Background()); err != nil {
		t.Fatalf("read bind: %v", err)
	}

	// Drive concurrent delivers that immediately time out, while the
	// client floods acks for every sequence. Without the fix, the
	// `defer cancelAck` runs *after* deliverAck has already closed the
	// channel, and on the second cancelAck path the runtime panics. With
	// the fix, sync.Once / atomic.Bool keeps the close single-owner.
	const N = 200
	var wg sync.WaitGroup
	for i := 1; i <= N; i++ {
		seq := uint64(i)
		// Client sends an ack with this seq; in flight before the deliver.
		wg.Add(1)
		go func() {
			defer wg.Done()
			ackCtx, ackCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer ackCancel()
			_ = conn.Write(ackCtx, codingws.MessageText,
				[]byte(`{"type":"ack","eventNumber":`+itoa(seq)+`}`))
		}()
		// Server-side deliver with a tight deadline so cancelAck races
		// the freshly-arrived deliverAck.
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := newEnvelope(subID, seq, []byte(`{"r":"x"}`))
			env.Deadline = time.Now().Add(2 * time.Millisecond)
			_, _ = ch.Deliver(context.Background(), env)
		}()
	}
	wg.Wait()
}

// itoa formats a non-negative integer for the test ack messages
// without pulling in strconv.
func itoa(u uint64) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

// Make sure the channel package compiles with our test imports — keep
// a non-test reference so `go vet` does not complain about unused
// imports if a test is later trimmed.
var _ = channel.OutcomeDelivered
