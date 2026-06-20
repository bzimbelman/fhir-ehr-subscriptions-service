// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-resthook-subscriber is a real HTTP service that the
// fhir-subs binary's rest-hook channel can deliver to. It runs inside
// the H1 realstack docker-compose stack and replaces the in-process
// e2e/mocksub/RestHookReceiver fake plus the legacy harness's
// hpipe.StartTLSRestHookServer wrapper.
//
// The binary captures every inbound request (method, path, header,
// body) and exposes a query API tests use to assert on observed
// deliveries. It also exposes a control-plane API tests use to install
// per-subscription response programs (status sequences, header
// injection, latency injection, mid-body close) — replacing the legacy
// harness's "wire your own http.Handler" pattern with a JSON DSL the
// docker-compose-deployed binary can execute.
//
// Routes:
//
//	POST   /program/{tag}                — install a response program for tag.
//	DELETE /program/{tag}                — clear the program for tag.
//	POST   /notify/{subscription_id}     — delivery target. Returns 200 by default.
//	POST   /hook/{subscription_id}       — alias for /notify/{id}.
//	GET    /notifications                — JSON array of every captured request.
//	GET    /notifications/{subscription_id} — JSON array filtered by sub id.
//	POST   /reset                        — clears the journal (per-test isolation).
//	GET    /healthz                      — liveness probe.
//
// The binary also emits the captured journal on stdout as JSON Lines
// so a test that prefers tail-following over the query API can do so
// without needing a sidecar.
//
// DSL — POST /program/{tag} body:
//
//	{
//	  "sequence": [
//	    { "status": 503, "headers": {"Retry-After": "1"}, "latency_ms": 0, "body": "" },
//	    { "status": 200 }
//	  ],
//	  "default_status": 200
//	}
//
// When `sequence` exhausts, requests fall back to `default_status` (200
// when omitted). `close_after_bytes` on a step truncates the response
// body mid-write — the connection is hijacked and aborted so the
// channel sees a partial write.
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

// journal holds every captured request plus per-tag response programs.
// The mutex guards mutation during concurrent inbound deliveries; reads
// return a copy so callers can iterate without racing further appends.
type journal struct {
	mu       sync.Mutex
	all      []ReceivedRequest
	wire     io.Writer
	programs map[string]*programState
}

func newJournal() *journal {
	return &journal{
		wire:     os.Stdout,
		programs: make(map[string]*programState),
	}
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

// program is the JSON DSL the control plane accepts.
type program struct {
	Sequence      []programStep `json:"sequence"`
	DefaultStatus int           `json:"default_status"`
}

// programStep is one entry in a program's response sequence.
type programStep struct {
	Status          int               `json:"status"`
	Headers         map[string]string `json:"headers"`
	Body            string            `json:"body"`
	LatencyMS       int               `json:"latency_ms"`
	CloseAfterBytes int               `json:"close_after_bytes"`
}

// programState tracks the installed program plus the next-step cursor.
type programState struct {
	prog   program
	cursor int
}

func (j *journal) installProgram(tag string, p program) {
	j.mu.Lock()
	j.programs[tag] = &programState{prog: p}
	j.mu.Unlock()
}

func (j *journal) clearProgram(tag string) {
	j.mu.Lock()
	delete(j.programs, tag)
	j.mu.Unlock()
}

// nextStep advances the cursor for tag and returns the step to play
// plus a bool indicating whether a program is installed at all.
func (j *journal) nextStep(tag string) (programStep, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	st, ok := j.programs[tag]
	if !ok {
		return programStep{}, false
	}
	if st.cursor < len(st.prog.Sequence) {
		step := st.prog.Sequence[st.cursor]
		st.cursor++
		return step, true
	}
	def := st.prog.DefaultStatus
	if def == 0 {
		def = http.StatusOK
	}
	return programStep{Status: def}, true
}

// extractTag returns the path segment immediately after prefix, or
// the empty string when prefix is absent.
func extractTag(path, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i]
	}
	return rest
}

// extractSubID derives the subscription id from /notify/{id} or
// /hook/{id}. Returns the empty string when neither prefix matches.
func extractSubID(path string) string {
	for _, prefix := range []string{"/notify/", "/hook/"} {
		if id := extractTag(path, prefix); id != "" {
			return id
		}
	}
	return ""
}

// buildMux assembles the HTTP routes for the binary. Exposed so tests
// can drive the server through httptest without reaching for the live
// listener.
func buildMux(jr *journal) http.Handler {
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

	mux.HandleFunc("/program/", func(w http.ResponseWriter, r *http.Request) {
		tag := extractTag(r.URL.Path, "/program/")
		if tag == "" {
			http.Error(w, "missing program tag", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
				return
			}
			var p program
			if err := json.Unmarshal(body, &p); err != nil {
				http.Error(w, "invalid program JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
			jr.installProgram(tag, p)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			jr.clearProgram(tag)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "POST/PUT/DELETE required", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/notifications", func(w http.ResponseWriter, _ *http.Request) {
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
		subID := extractSubID(r.URL.Path)
		jr.append(ReceivedRequest{
			SubscriptionID: subID,
			ReceivedAt:     time.Now().UTC(),
			Method:         r.Method,
			Path:           r.URL.Path,
			Header:         r.Header.Clone(),
			Body:           string(body),
		})

		if subID != "" {
			if step, ok := jr.nextStep(subID); ok {
				playStep(w, r, step)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "delivered")
	})

	return mux
}

// playStep writes one programmed response to the client. Handles
// latency injection, header injection, status code, and mid-body
// connection abort.
func playStep(w http.ResponseWriter, r *http.Request, step programStep) {
	if step.LatencyMS > 0 {
		select {
		case <-time.After(time.Duration(step.LatencyMS) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	status := step.Status
	if status == 0 {
		status = http.StatusOK
	}

	if step.CloseAfterBytes > 0 {
		// Hijack the connection so we can write a partial body and
		// abort it mid-stream — the channel must see a truncated
		// response and treat it as a delivery failure.
		hj, ok := w.(http.Hijacker)
		if !ok {
			for k, v := range step.Headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			truncate := step.Body
			if step.CloseAfterBytes < len(truncate) {
				truncate = truncate[:step.CloseAfterBytes]
			}
			_, _ = io.WriteString(w, truncate)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
		for k, v := range step.Headers {
			fmt.Fprintf(buf, "%s: %s\r\n", k, v)
		}
		fmt.Fprintf(buf, "Connection: close\r\n\r\n")
		truncate := step.Body
		if step.CloseAfterBytes < len(truncate) {
			truncate = truncate[:step.CloseAfterBytes]
		}
		_, _ = buf.WriteString(truncate)
		_ = buf.Flush()
		return
	}

	for k, v := range step.Headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	if step.Body != "" {
		_, _ = io.WriteString(w, step.Body)
	}
}

func main() {
	addr := flag.String("addr", ":8090", "HTTP listener address")
	flag.Parse()

	jr := newJournal()

	log.Printf("test-resthook-subscriber listening on %s", *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           buildMux(jr),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
