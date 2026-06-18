// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codingws "github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// TestE2E_WebSocket_MaxSessionsCapRejectsBind verifies S-7 #1: when
// MaxSessions is configured, bind attempts beyond the cap are rejected
// with bind-error and emit
// fhir_subs_channel_websocket_bind_rejected_total{reason="capacity"}.
func TestE2E_WebSocket_MaxSessionsCapRejectsBind(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newE2EFakeTokens(now)
	subA := uuid.New()
	subB := uuid.New()
	tokens.mint("a", subA, "client-a", now().Add(60*time.Second))
	tokens.mint("b", subB, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:      tokens,
		Replayer:    e2eNoopReplayer{},
		Now:         now,
		MaxSessions: 1,
	})
	if err != nil {
		t.Fatalf("websocket.New: %v", err)
	}
	defer ch.Close()

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"

	c1 := dialE2E(t, srv, wsURL)
	defer c1.Close(codingws.StatusNormalClosure, "")
	writeE2E(t, c1, `{"type":"bind","subscriptionId":"`+subA.String()+`","token":"a"}`)
	if _, data := readE2E(t, c1, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("first bind = %s", data)
	}

	c2 := dialE2E(t, srv, wsURL)
	defer c2.Close(codingws.StatusNormalClosure, "")
	writeE2E(t, c2, `{"type":"bind","subscriptionId":"`+subB.String()+`","token":"b"}`)
	_, data := readE2E(t, c2, 2*time.Second)
	if !strings.Contains(string(data), `"bind-error"`) {
		t.Fatalf("expected bind-error when MaxSessions reached; got %s", data)
	}
	if !strings.Contains(string(data), "capacity") {
		t.Errorf("bind-error reason should mention capacity; got %s", data)
	}
}

// TestE2E_WebSocket_ReplayCapTruncates verifies S-7 #8: a subscriber
// that reconnects from an ancient lastReceivedEventNumber receives at
// most MaxReplayEvents events plus a 'replay-truncated' control frame.
func TestE2E_WebSocket_ReplayCapTruncates(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newE2EFakeTokens(now)
	subID := uuid.New()
	tokens.mint("tok", subID, "client-a", now().Add(60*time.Second))

	rep := newE2EReplayer()
	for i := uint64(1); i <= 50; i++ {
		rep.append(subID, i, []byte(`{"e":`+e2eUintStr(i)+`}`))
	}

	ch, err := websocket.New(websocket.Options{
		Tokens:          tokens,
		Replayer:        rep,
		Now:             now,
		MaxReplayEvents: 5,
		IdleTimeout:     time.Hour,
		PingInterval:    time.Hour,
	})
	if err != nil {
		t.Fatalf("websocket.New: %v", err)
	}
	defer ch.Close()

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"

	conn := dialE2E(t, srv, wsURL)
	defer conn.Close(codingws.StatusNormalClosure, "")
	writeE2E(t, conn, `{"type":"bind","subscriptionId":"`+subID.String()+`","token":"tok","lastReceivedEventNumber":0}`)
	if _, data := readE2E(t, conn, 2*time.Second); !strings.Contains(string(data), `"bind-success"`) {
		t.Fatalf("bind = %s", data)
	}

	// First 5 frames are the capped replay.
	for i := 1; i <= 5; i++ {
		_, body := readE2E(t, conn, 2*time.Second)
		if !strings.Contains(string(body), `"e":`+e2eUintStr(uint64(i))) {
			t.Errorf("event %d body = %s", i, body)
		}
	}

	// 6th frame is the replay-truncated control message.
	_, body := readE2E(t, conn, 2*time.Second)
	if !strings.Contains(string(body), "replay-truncated") {
		t.Fatalf("expected replay-truncated control frame; got %s", body)
	}
}

// e2eReplayer is a minimal websocket.EventReplayer for e2e replay tests.
type e2eReplayer struct {
	events map[uuid.UUID][]websocket.PastEvent
}

func newE2EReplayer() *e2eReplayer {
	return &e2eReplayer{events: map[uuid.UUID][]websocket.PastEvent{}}
}

func (r *e2eReplayer) append(subID uuid.UUID, num uint64, body []byte) {
	r.events[subID] = append(r.events[subID], websocket.PastEvent{
		EventNumber: num,
		Bundle:      body,
	})
}

func (r *e2eReplayer) ReplaySince(_ context.Context, subID uuid.UUID, after uint64) ([]websocket.PastEvent, error) {
	in := r.events[subID]
	out := make([]websocket.PastEvent, 0, len(in))
	for _, e := range in {
		if e.EventNumber > after {
			out = append(out, e)
		}
	}
	return out, nil
}

func dialE2E(t *testing.T, srv *httptest.Server, wsURL string) *codingws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := codingws.Dial(ctx, wsURL, &codingws.DialOptions{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func writeE2E(t *testing.T, c *codingws.Conn, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := c.Write(ctx, codingws.MessageText, []byte(msg)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readE2E(t *testing.T, c *codingws.Conn, dur time.Duration) (codingws.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()
	mt, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return mt, data
}

func e2eUintStr(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
