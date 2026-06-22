// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// websocket subsystem of cmd/test-receivers — opens a real WS
// connection to the prod binary's /ws/subscriptions endpoint, captures
// every event, and exposes a query API at /events for realstack
// tests to assert on observed deliveries.
//
// Lifted unchanged in behaviour from cmd/test-ws-subscriber/main.go.
// OP #345 collapses the four receivers into one process.
package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Event is one captured inbound WS message.
type Event struct {
	SubscriptionID string    `json:"subscription_id"`
	ReceivedAt     time.Time `json:"received_at"`
	Type           string    `json:"type"`
	Body           string    `json:"body"`
}

type wsJournal struct {
	mu   sync.Mutex
	all  []Event
	wire io.Writer
}

func (j *wsJournal) append(e Event) {
	j.mu.Lock()
	j.all = append(j.all, e)
	j.mu.Unlock()
	if j.wire != nil {
		_ = json.NewEncoder(j.wire).Encode(e)
	}
}

func (j *wsJournal) snapshot() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Event, len(j.all))
	copy(out, j.all)
	return out
}

func (j *wsJournal) reset() {
	j.mu.Lock()
	j.all = nil
	j.mu.Unlock()
}

// connectAndReceive opens the WS stream and pumps events into jr until
// ctx cancels or the server closes. Reconnect logic is intentionally
// simple: log the close, wait one second, retry. Tests that exercise
// reconnect semantics drive the reconnect via the binary, not via
// killing the subscriber.
func connectAndReceive(ctx context.Context, url, token, topic string, jr *wsJournal) {
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
		if token != "" {
			opts.HTTPHeader.Set("Authorization", "Bearer "+token)
		}
		conn, _, err := websocket.Dial(ctx, url, opts)
		if err != nil {
			log.Printf("ws dial %s: %v; retrying in 1s", url, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}
		log.Printf("ws connected to %s", url)

		// Send the bind frame the binary expects: a subscription_id
		// + topic envelope. The binary's WS handler reads the first
		// JSON frame as the bind request.
		if topic != "" {
			bind := map[string]string{"type": "bind", "topic": topic}
			if data, err := json.Marshal(bind); err == nil {
				_ = conn.Write(ctx, websocket.MessageText, data)
			}
		}

		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				log.Printf("ws read: %v", err)
				_ = conn.CloseNow()
				break
			}
			subID := ""
			var envelope struct {
				SubscriptionID string `json:"subscription_id"`
			}
			if json.Unmarshal(data, &envelope) == nil {
				subID = envelope.SubscriptionID
			}
			jr.append(Event{
				SubscriptionID: subID,
				ReceivedAt:     time.Now().UTC(),
				Type:           typ.String(),
				Body:           string(data),
			})
		}
	}
}

// buildWSMux returns the HTTP query API for the websocket subsystem.
// /events, /events/{id}, /reset, /healthz mirror the legacy
// cmd/test-ws-subscriber routes 1:1.
func buildWSMux(jr *wsJournal) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("/reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		jr.reset()
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jr.snapshot())
	})

	mux.HandleFunc("/events/", func(w http.ResponseWriter, r *http.Request) {
		want := strings.TrimPrefix(r.URL.Path, "/events/")
		all := jr.snapshot()
		filtered := all[:0]
		for _, e := range all {
			if e.SubscriptionID == want {
				filtered = append(filtered, e)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(filtered)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "use /events or /healthz", http.StatusNotFound)
	})

	return mux
}
