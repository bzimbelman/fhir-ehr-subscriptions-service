// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-resthook-subscriber is a real HTTP service that the
// fhir-subs binary's rest-hook channel can deliver to. It runs inside
// the H1 realstack docker-compose stack and replaces the in-process
// e2e/mocksub/RestHookReceiver fake.
//
// The binary captures every inbound request (method, path, header,
// body) and exposes a query API tests use to assert on observed
// deliveries:
//
//	POST /notify/{subscription_id}        — delivery target. Returns 200.
//	GET  /notifications                   — JSON array of every captured request.
//	GET  /notifications/{subscription_id} — JSON array filtered by sub id.
//	POST /reset                           — clears the journal (per-test isolation).
//	GET  /healthz                         — liveness probe.
//
// The binary also emits the captured journal on stdout as JSON Lines
// so a test that prefers tail-following over the query API can do so
// without needing a sidecar.
package main

import (
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
)

// ReceivedRequest is one captured inbound HTTP request.
type ReceivedRequest struct {
	SubscriptionID string      `json:"subscription_id"`
	ReceivedAt     time.Time   `json:"received_at"`
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	Header         http.Header `json:"header"`
	Body           string      `json:"body"`
}

// journal holds every captured request. The mutex guards mutation
// during concurrent inbound deliveries; reads return a copy so callers
// can iterate without racing further appends.
type journal struct {
	mu   sync.Mutex
	all  []ReceivedRequest
	wire io.Writer
}

func (j *journal) append(r ReceivedRequest) {
	j.mu.Lock()
	j.all = append(j.all, r)
	j.mu.Unlock()
	if j.wire != nil {
		_ = json.NewEncoder(j.wire).Encode(r)
	}
}

func (j *journal) snapshot() []ReceivedRequest {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]ReceivedRequest, len(j.all))
	copy(out, j.all)
	return out
}

func (j *journal) reset() {
	j.mu.Lock()
	j.all = nil
	j.mu.Unlock()
}

// extractSubID derives the subscription id from /notify/{id} (or
// /hook/{id}, which the legacy e2e suite also targets). Returns the
// empty string when the path doesn't match either prefix.
func extractSubID(path string) string {
	for _, prefix := range []string{"/notify/", "/hook/"} {
		if strings.HasPrefix(path, prefix) {
			rest := strings.TrimPrefix(path, prefix)
			if i := strings.Index(rest, "/"); i >= 0 {
				return rest[:i]
			}
			return rest
		}
	}
	return ""
}

func main() {
	addr := flag.String("addr", ":8090", "HTTP listener address")
	flag.Parse()

	jr := &journal{wire: os.Stdout}

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

	mux.HandleFunc("/notifications", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jr.snapshot())
	})

	mux.HandleFunc("/notifications/", func(w http.ResponseWriter, r *http.Request) {
		want := strings.TrimPrefix(r.URL.Path, "/notifications/")
		all := jr.snapshot()
		filtered := all[:0]
		for _, req := range all {
			if req.SubscriptionID == want {
				filtered = append(filtered, req)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(filtered)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		jr.append(ReceivedRequest{
			SubscriptionID: extractSubID(r.URL.Path),
			ReceivedAt:     time.Now().UTC(),
			Method:         r.Method,
			Path:           r.URL.Path,
			Header:         r.Header.Clone(),
			Body:           string(body),
		})
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "delivered")
	})

	log.Printf("test-resthook-subscriber listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
