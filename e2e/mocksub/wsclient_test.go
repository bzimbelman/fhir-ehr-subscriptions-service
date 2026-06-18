// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The WSS client is the subscriber side of fhir-subs' WebSocket channel.
// In production, fhir-subs is the server and the subscriber connects to
// it. The mock owns:
//
//   * A Dial method that opens the connection and starts reading frames
//     into a journal.
//   * A Frames() snapshot accessor for the orchestrator.
//   * Clean shutdown via context cancellation.

func TestWSClient_DialsAndJournalsFrames(t *testing.T) {
	t.Parallel()
	// Stand up a tiny WS server that accepts an upgrade and writes one
	// notification-shape frame.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"handshake"}`))
		_ = c.Write(r.Context(), websocket.MessageText, []byte(`{"type":"notification-event","eventNumber":1}`))
		// Wait for the client to close.
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	client := NewWSClient(wsURL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Dial(ctx); err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Wait for at least 2 journaled frames.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(client.Frames()) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	frames := client.Frames()
	if len(frames) < 2 {
		t.Fatalf("frames: got %d want >=2", len(frames))
	}
	if !strings.Contains(string(frames[0]), `"handshake"`) {
		t.Fatalf("first frame: %q", frames[0])
	}
	if !strings.Contains(string(frames[1]), `"notification-event"`) {
		t.Fatalf("second frame: %q", frames[1])
	}
}
