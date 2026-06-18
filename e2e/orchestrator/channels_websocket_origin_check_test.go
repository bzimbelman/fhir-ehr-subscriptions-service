// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

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

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/websocket"
)

// TestE2E_WebSocket_OriginRejected verifies B-17: a WS upgrade request
// presenting a foreign Origin is rejected by the channel's upgrade
// handler with HTTP 403 (and the connection never reaches bind).
// The same channel accepts a connection from a configured trusted
// Origin.
func TestE2E_WebSocket_OriginRejected(t *testing.T) {
	t.Parallel()

	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	tokens := newE2EFakeTokens(now)
	subID := uuid.New()
	tokens.mint("ok-token", subID, "client-a", now().Add(60*time.Second))

	ch, err := websocket.New(websocket.Options{
		Tokens:         tokens,
		Replayer:       e2eNoopReplayer{},
		Now:            now,
		OriginPatterns: []string{"trusted.example"},
	})
	if err != nil {
		t.Fatalf("websocket.New: %v", err)
	}
	defer ch.Close()

	srv := httptest.NewServer(ch.Handler())
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws/subscriptions"

	// Foreign Origin -> 403.
	{
		hdr := http.Header{}
		hdr.Set("Origin", "https://attacker.example")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, resp, err := codingws.Dial(ctx, wsURL, &codingws.DialOptions{
			HTTPClient: srv.Client(),
			HTTPHeader: hdr,
		})
		if err == nil {
			_ = conn.Close(codingws.StatusNormalClosure, "")
			t.Fatalf("foreign Origin upgrade succeeded; want rejection")
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %v; want 403", resp)
		}
	}

	// Trusted Origin -> bind succeeds.
	{
		hdr := http.Header{}
		hdr.Set("Origin", "https://trusted.example")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		conn, _, err := codingws.Dial(ctx, wsURL, &codingws.DialOptions{
			HTTPClient: srv.Client(),
			HTTPHeader: hdr,
		})
		if err != nil {
			t.Fatalf("trusted Origin dial: %v", err)
		}
		defer conn.Close(codingws.StatusNormalClosure, "")
		if err := conn.Write(ctx, codingws.MessageText,
			[]byte(`{"type":"bind","subscriptionId":"`+subID.String()+`","token":"ok-token"}`)); err != nil {
			t.Fatalf("write bind: %v", err)
		}
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read bind: %v", err)
		}
		if !strings.Contains(string(data), `"bind-success"`) {
			t.Fatalf("trusted bind reply = %s; want bind-success", data)
		}
	}
}

// --- shared in-memory ws fakes (e2e test scope) ---

type e2eFakeTokens struct {
	mu   sync.Mutex
	rows map[string]*e2eTokenRow
	now  func() time.Time
}

type e2eTokenRow struct {
	subID    uuid.UUID
	clientID string
	expires  time.Time
	consumed bool
}

func newE2EFakeTokens(now func() time.Time) *e2eFakeTokens {
	return &e2eFakeTokens{rows: map[string]*e2eTokenRow{}, now: now}
}

func (f *e2eFakeTokens) mint(token string, sub uuid.UUID, client string, exp time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[token] = &e2eTokenRow{subID: sub, clientID: client, expires: exp}
}

func (f *e2eFakeTokens) Consume(_ context.Context, token string, now time.Time) (websocket.ConsumeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[token]
	if !ok {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeNotFound}, nil
	}
	if r.consumed {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeAlreadyUsed}, nil
	}
	if !r.expires.After(now) {
		return websocket.ConsumeResult{Outcome: websocket.ConsumeExpired}, nil
	}
	r.consumed = true
	return websocket.ConsumeResult{
		Outcome:        websocket.ConsumeOK,
		SubscriptionID: r.subID,
		ClientID:       r.clientID,
	}, nil
}

type e2eNoopReplayer struct{}

func (e2eNoopReplayer) ReplaySince(context.Context, uuid.UUID, uint64) ([]websocket.PastEvent, error) {
	return nil, nil
}
