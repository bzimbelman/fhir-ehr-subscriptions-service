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

	if werr := conn.Write(ctx, codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ok-token"}`)); werr != nil {
		t.Fatalf("write bind: %v", werr)
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
// closing an already-closed channel. The race is between the Deliver
// path's cleanup and deliverAck firing for the same sequence. We drive
// many iterations sequentially through a single client connection (so
// reads/writes on the *codingws.Conn stay serialized), interleaving:
//
//   - server-side Deliver with a tight ack deadline,
//   - the client sending an ack BEFORE the deadline some of the time
//     and AFTER the deadline others.
//
// Even though each iteration is sequential on the client, multiple
// stray acks (duplicated client ack frames for the same sequence) and
// the server's own cleanup defer must not race to close-of-closed.
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
		AckTimeout: 5 * time.Millisecond,
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

	// Run iterations sequentially per-connection. Each iteration kicks
	// a Deliver in a goroutine, then writes an ack frame from the test
	// goroutine. Because the test goroutine owns reads+writes on the
	// conn, we avoid client-side data races; the server's cleanup vs.
	// deliverAck race remains the actual subject under test.
	const N = 200
	for i := 1; i <= N; i++ {
		seq := uint64(i)
		// Server-side Deliver: tight deadline so the cleanup (cancelAck
		// + closeOnce) races whatever ack the client is about to send.
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := newEnvelope(subID, seq, []byte(`{"r":"x"}`))
			env.Deadline = time.Now().Add(15 * time.Millisecond)
			_, _ = ch.Deliver(context.Background(), env)
		}()

		// Drain the inbound notification frame so the per-message read
		// loop on the server keeps draining.
		readCtx, readCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, _, _ = conn.Read(readCtx)
		readCancel()

		// Send the matching ack (some on time, some after the deadline).
		ackCtx, ackCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_ = conn.Write(ackCtx, codingws.MessageText,
			[]byte(`{"type":"ack","eventNumber":`+itoa(seq)+`}`))
		// Send a duplicate ack for the SAME sequence: previously this
		// could re-enter close on a recycled channel.
		_ = conn.Write(ackCtx, codingws.MessageText,
			[]byte(`{"type":"ack","eventNumber":`+itoa(seq)+`}`))
		ackCancel()

		wg.Wait()
	}
}

// B-18: A direct race on cancelAck and deliverAck on the same sequence
// must not panic. Exercised by sending duplicate acks in lockstep while
// the matching Deliver goroutine times out.
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
		AckTimeout: 2 * time.Millisecond,
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

	bctx, bcancel := context.WithTimeout(context.Background(), 1*time.Second)
	if err := conn.Write(bctx, codingws.MessageText,
		[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ack-cancel"}`)); err != nil {
		t.Fatalf("write bind: %v", err)
	}
	if _, _, err := conn.Read(bctx); err != nil {
		t.Fatalf("read bind: %v", err)
	}
	bcancel()

	// Sequentially drive 200 deliver+ack iterations. The deliver path
	// hits its 2ms ack deadline; the client ack we then send fires on
	// a stale waiter (the cleanup already removed it). Without the fix,
	// duplicate client acks could double-close the channel.
	const N = 200
	for i := 1; i <= N; i++ {
		seq := uint64(i)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := newEnvelope(subID, seq, []byte(`{"r":"x"}`))
			env.Deadline = time.Now().Add(2 * time.Millisecond)
			_, _ = ch.Deliver(context.Background(), env)
		}()

		readCtx, readCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		_, _, _ = conn.Read(readCtx)
		readCancel()

		ackCtx, ackCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		// Send three acks in a row: a stale waiter could double-close
		// without sync.Once on the channel close path.
		for k := 0; k < 3; k++ {
			_ = conn.Write(ackCtx, codingws.MessageText,
				[]byte(`{"type":"ack","eventNumber":`+itoa(seq)+`}`))
		}
		ackCancel()
		wg.Wait()
	}
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
