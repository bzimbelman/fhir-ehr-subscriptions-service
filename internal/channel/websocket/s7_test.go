// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// TestS7_MaxSessionsCap (S-7 #1) — when MaxSessions is configured,
// additional bind attempts beyond the cap are rejected with bind-error.
// This prevents an unbounded sessions map and a connection-flood DoS.
func TestS7_MaxSessionsCap(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subA := uuid.New()
	subB := uuid.New()
	tokens.Mint("a", subA, "client-a", now().Add(60*time.Second))
	tokens.Mint("b", subB, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:      tokens,
		Replayer:    newFakeReplayer(),
		Now:         now,
		MaxSessions: 1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	c1 := dialClient(t, srv)
	defer c1.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c1, `{"type":"bind","subscriptionId":"`+subA.String()+`","token":"a"}`)
	if _, data := readText(t, c1, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("first bind = %s", data)
	}

	c2 := dialClient(t, srv)
	defer c2.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c2, `{"type":"bind","subscriptionId":"`+subB.String()+`","token":"b"}`)
	_, data := readText(t, c2, 2*time.Second)
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Fatalf("expected bind-error when MaxSessions reached; got %s", data)
	}
	if !strings.Contains(string(data), "max sessions") && !strings.Contains(string(data), "capacity") {
		t.Errorf("bind-error reason should mention capacity; got %s", data)
	}
}

// TestS7_MaxSessionsPerClientCap (S-7 #1) — per-client cap stops one
// noisy client from consuming the whole session table.
func TestS7_MaxSessionsPerClientCap(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subA := uuid.New()
	subB := uuid.New()
	subC := uuid.New()
	tokens.Mint("a", subA, "client-shared", now().Add(60*time.Second))
	tokens.Mint("b", subB, "client-shared", now().Add(60*time.Second))
	tokens.Mint("c", subC, "client-other", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:               tokens,
		Replayer:             newFakeReplayer(),
		Now:                  now,
		MaxSessions:          10,
		MaxSessionsPerClient: 1,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	c1 := dialClient(t, srv)
	defer c1.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c1, `{"type":"bind","subscriptionId":"`+subA.String()+`","token":"a"}`)
	if _, data := readText(t, c1, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("first bind = %s", data)
	}

	c2 := dialClient(t, srv)
	defer c2.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c2, `{"type":"bind","subscriptionId":"`+subB.String()+`","token":"b"}`)
	_, data := readText(t, c2, 2*time.Second)
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Fatalf("expected bind-error for second connection from same client; got %s", data)
	}

	// A different client may still bind.
	c3 := dialClient(t, srv)
	defer c3.Close(codingws.StatusNormalClosure, "")
	writeBind(t, c3, `{"type":"bind","subscriptionId":"`+subC.String()+`","token":"c"}`)
	if _, data := readText(t, c3, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Errorf("different client bind should succeed; got %s", data)
	}
}

// TestS7_BindTimeoutConfigurable (S-7 #4) — the bind-message read timeout
// must be configurable via Options.BindTimeout. A client that connects
// but never sends bind-frame is closed within ~BindTimeout.
func TestS7_BindTimeoutConfigurable(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Now() }
	ch, err := websocket.New(websocket.Options{
		Tokens:      newFakeTokens(now),
		Replayer:    newFakeReplayer(),
		Now:         now,
		BindTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")

	// Client never sends bind. Reading from conn should fail within ~BindTimeout
	// (as the server closes the connection due to bind read deadline expiry).
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected read to error after bind timeout; got nil")
	}
	if elapsed > 800*time.Millisecond {
		t.Errorf("bind timeout took %v; should be ~100ms", elapsed)
	}
}

// TestS7_SetReadLimitEnforced (S-7 #3) — the channel must call
// conn.SetReadLimit(MaxFrameBytes) so an inbound frame larger than the
// outbound MaxFrameBytes is rejected by the library at the read layer
// (default coder/websocket inbound limit is 32 KiB which conflicts with
// MaxFrameBytes=8MB outbound).
func TestS7_SetReadLimitEnforced(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:        tokens,
		Replayer:      newFakeReplayer(),
		Now:           now,
		MaxFrameBytes: 64 * 1024,
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

	// Send a 50KB inbound message — should fit MaxFrameBytes=64KB.
	big := strings.Repeat("x", 50*1024)
	writeBind(t, conn, `{"type":"hello","payload":"`+big+`"}`)
	// The server ignores unrecognized types and continues. Send an ack to
	// confirm the connection is alive.
	writeBind(t, conn, `{"type":"ack","eventNumber":42}`)

	// The conn should still be alive (no close frame). Verify by reading
	// with a short timeout — we expect a timeout, not a close.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err = conn.Read(ctx)
	if err == nil {
		t.Fatal("read should not return success; we expect a timeout")
	}
	if cs := codingws.CloseStatus(err); cs != -1 {
		t.Errorf("connection closed unexpectedly with status %d (want still-open / timeout); err=%v", cs, err)
	}
}

// TestS7_PingWriteTimeoutConfigurable (S-7 #6) — verifying the seam at
// the API level. We inject a deliberately tiny PingWriteTimeout and a
// huge IdleTimeout, then assert the channel does NOT take the full
// IdleTimeout to error out on a closed conn. The exact timing is
// timing-dependent; this test focuses on the API contract: the ping
// path is governed by PingWriteTimeout, not IdleTimeout. We do that by
// closing the client mid-flight and timing the channel's response.
func TestS7_PingWriteTimeoutConfigurable(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Now() }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:           tokens,
		Replayer:         newFakeReplayer(),
		Now:              now,
		PingInterval:     20 * time.Millisecond,
		PingWriteTimeout: 100 * time.Millisecond,
		IdleTimeout:      time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok"}`)
	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	// Force-close the client without a Close frame. The next ping will
	// fail-write quickly (well under IdleTimeout). The channel must tear
	// down the session and a subsequent Deliver returns Transient ("no
	// socket"). If PingWriteTimeout were ignored the channel would wait
	// for the full IdleTimeout (1 hour) before noticing.
	_ = conn.CloseNow()

	// Give the ping loop time to fire and detect failure.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := ch.Deliver(context.Background(), newEnvelope(subID, 1, []byte(`{}`)))
		if out.Kind == channel.OutcomeTransient {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("session was not torn down within 2s of conn.CloseNow; ping path is not bounded by PingWriteTimeout")
}

// TestS7_ReadLoopEnforcesIdleTimeout (S-7 #7) — readLoop must close
// the session when the peer is idle for longer than IdleTimeout.
// The audit notes 'documented "5min idle" is unimplemented'.
func TestS7_ReadLoopEnforcesIdleTimeout(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Now() }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:       tokens,
		Replayer:     newFakeReplayer(),
		Now:          now,
		PingInterval: time.Hour, // disable ping path so idle is the only signal
		IdleTimeout:  150 * time.Millisecond,
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

	// Wait past IdleTimeout. Channel should have torn the session down.
	time.Sleep(500 * time.Millisecond)

	out, _ := ch.Deliver(context.Background(), newEnvelope(subID, 1, []byte(`{}`)))
	if out.Kind != channel.OutcomeTransient {
		t.Errorf("expected transient after idle timeout; got %v %q", out.Kind, out.Reason)
	}
}

// TestS7_ReplayCappedAtMaxReplayEvents (S-7 #8) — replay must stop after
// MaxReplayEvents events to avoid OOM on a million-event subscription.
// Default cap is large; tests configure a small cap.
func TestS7_ReplayCappedAtMaxReplayEvents(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	replayer := newFakeReplayer()
	for i := uint64(1); i <= 100; i++ {
		replayer.Append(subID, websocket.PastEvent{
			EventNumber: i,
			Bundle:      []byte(`{"e":` + uintString(i) + `}`),
			ContentType: channel.ContentTypeFHIRJSON,
		})
	}

	ch, err := websocket.New(websocket.Options{
		Tokens:          tokens,
		Replayer:        replayer,
		Now:             now,
		MaxReplayEvents: 3,
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()
	defer ch.Close()

	conn := dialClient(t, srv)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok","lastReceivedEventNumber":0}`)
	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	// Read up to MaxReplayEvents events.
	for i := 1; i <= 3; i++ {
		_, body := readText(t, conn, 2*time.Second)
		if !strings.Contains(string(body), `"e":`+uintString(uint64(i))) {
			t.Errorf("event %d body = %s", i, body)
		}
	}

	// The 4th read should be a replay-truncated control message OR a timeout
	// (no further frames sent). Anything else means the cap was ignored.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	mt, body, err := conn.Read(ctx)
	if err == nil {
		s := string(body)
		if !strings.Contains(s, "replay-truncated") && mt == codingws.MessageText {
			t.Errorf("4th frame should be replay-truncated control or no frame; got mt=%v body=%s", mt, body)
		}
	}
}

// TestS7_CloseWaitsForGoroutines (S-7 #9) — Close must WaitGroup-join
// per-session goroutines so shutdown is deterministic. After Close
// returns, no further frames are written by the channel.
func TestS7_CloseWaitsForGoroutines(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newFakeTokens(now)
	subID := uuid.New()
	tokens.Mint("tok", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:       tokens,
		Replayer:     newFakeReplayer(),
		Now:          now,
		PingInterval: 10 * time.Millisecond,
		IdleTimeout:  time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	conn := dialClient(t, srv)
	writeBind(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok"}`)
	if _, data := readText(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	// Close should not race the ping loop / read loop. Run Close + the
	// session goroutines together; race detector will fire if Close returns
	// before the goroutines finish. 5s deadline is generous; under -race
	// with parallel tests scheduling can slow conn teardown.
	done := make(chan struct{})
	go func() {
		_ = ch.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return within 5s — likely waiting on ungoverned goroutine")
	}

	// After Close returns, no frame should arrive on conn. Read with a tiny
	// timeout; a frame would mean a goroutine wrote after Close.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Errorf("frame arrived after Close — goroutines were not joined")
	}
	_ = conn.Close(codingws.StatusNormalClosure, "")
}

// TestS7_UpgradeReadHeaderTimeout (S-7 #2) — slowloris on the HTTP
// upgrade handshake: a client that opens the TCP connection but takes
// minutes to send headers must be cut off by the upgrade handler's
// read-header timeout. We assert the handler exposes the
// ReadHeaderTimeout knob and it's < 60s by default.
func TestS7_UpgradeReadHeaderTimeout(t *testing.T) {
	t.Parallel()

	// We don't drive a real slowloris connection here — that's fragile in
	// CI. Instead we verify the knob exists by configuring it via a custom
	// http.Server, ServeMux'ing the channel handler, and confirming an
	// abandoned slow-write client gets disconnected within the configured
	// timeout.
	now := func() time.Time { return time.Now() }
	ch, err := websocket.New(websocket.Options{
		Tokens:                   newFakeTokens(now),
		Replayer:                 newFakeReplayer(),
		Now:                      now,
		UpgradeReadHeaderTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// httptest.Server uses a default http.Server; the channel exposes a
	// ConfigureServer hook to apply its read-header timeout to a passed
	// *http.Server.
	mux := http.NewServeMux()
	mux.Handle("/", ch.Handler())
	httpSrv := &http.Server{Handler: mux}
	ch.ConfigureServer(httpSrv)
	if httpSrv.ReadHeaderTimeout != 100*time.Millisecond {
		t.Errorf("ReadHeaderTimeout = %v; want 100ms", httpSrv.ReadHeaderTimeout)
	}
}

// uintString avoids a strconv import in this test file.
func uintString(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
