// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-ws-subscriber is a real WebSocket client that connects
// to the fhir-subs binary's WSS endpoint, captures every event the
// binary pushes, and exposes a query API tests use to assert on
// observed deliveries.
//
// Configuration is environment-driven (the docker-compose service
// passes them in):
//
//	WS_URL                  — wss://fhir-subs:8443/ws/subscriptions, set per test
//	WS_BINDING_TOKEN        — bearer token minted by the binary's $get-ws-binding-token
//	WS_SUBSCRIPTION_TOPIC   — topic URL to bind to
//	QUERY_API_ADDR          — bind addr for the query API (default :8091)
//
// Query API:
//
//	GET  /events                    — JSON array of every captured event
//	GET  /events/{subscription_id}  — filtered by id
//	POST /reset                     — clears the journal
//	GET  /healthz                   — liveness probe (returns 200 even pre-connect)
//
// Captured events are also emitted on stdout as JSON Lines so tests
// that prefer tail-following can do so without the query API.
//
// Designed to run as a real container in docker-compose; replaces the
// in-process e2e/mocksub WS client fake.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

type journal struct {
	mu   sync.Mutex
	all  []Event
	wire io.Writer
}

func (j *journal) append(e Event) {
	j.mu.Lock()
	j.all = append(j.all, e)
	j.mu.Unlock()
	if j.wire != nil {
		_ = json.NewEncoder(j.wire).Encode(e)
	}
}

func (j *journal) snapshot() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Event, len(j.all))
	copy(out, j.all)
	return out
}

func (j *journal) reset() {
	j.mu.Lock()
	j.all = nil
	j.mu.Unlock()
}

// connectAndReceive opens the WS stream and pumps events into jr until
// ctx cancels or the server closes. Reconnect logic is intentionally
// simple: log the close, wait one second, retry. Tests that exercise
// reconnect semantics drive the reconnect via the binary, not via
// killing the subscriber.
func connectAndReceive(ctx context.Context, url, token, topic string, jr *journal) {
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

func main() {
	addr := flag.String("addr", ":8091", "query API listener address")
	flag.Parse()

	wsURL := os.Getenv("WS_URL")
	wsToken := os.Getenv("WS_BINDING_TOKEN")
	wsTopic := os.Getenv("WS_SUBSCRIPTION_TOPIC")
	if v := os.Getenv("QUERY_API_ADDR"); v != "" {
		*addr = v
	}

	jr := &journal{wire: os.Stdout}

	ctx, cancel := context.WithCancel(context.Background())

	if wsURL != "" {
		go connectAndReceive(ctx, wsURL, wsToken, wsTopic, jr)
	} else {
		log.Printf("WS_URL not set; subscriber idle (query API only)")
	}

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

	log.Printf("test-ws-subscriber query API listening on %s", *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	err := srv.ListenAndServe()
	cancel()
	if err != nil {
		log.Printf("listen: %v", err)
		os.Exit(1)
	}
	_ = fmt.Sprint
}
