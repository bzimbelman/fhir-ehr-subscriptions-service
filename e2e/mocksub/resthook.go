// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mocksub

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ReceivedNotification is one journaled inbound POST to /hook/{sub}.
type ReceivedNotification struct {
	SubscriptionID string      `json:"subscription_id"`
	ReceivedAt     time.Time   `json:"received_at"`
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	Header         http.Header `json:"header"`
	Body           []byte      `json:"body"`
}

// receivedAlias is the wire shape of ReceivedNotification: Body is a
// UTF-8 string field (more readable in logs than a base64 []byte).
type receivedAlias struct {
	SubscriptionID string      `json:"subscription_id"`
	ReceivedAt     time.Time   `json:"received_at"`
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	Header         http.Header `json:"header"`
	Body           string      `json:"body"`
}

// MarshalJSON renders the body as a UTF-8 string field; the harness only
// cares about JSON FHIR Bundles for v1, and a string is more readable in
// orchestrator logs than a base64 byte array.
func (r ReceivedNotification) MarshalJSON() ([]byte, error) {
	return json.Marshal(receivedAlias{
		SubscriptionID: r.SubscriptionID,
		ReceivedAt:     r.ReceivedAt,
		Method:         r.Method,
		Path:           r.Path,
		Header:         r.Header,
		Body:           string(r.Body),
	})
}

// UnmarshalJSON decodes the wire shape (Body as string) back into a
// ReceivedNotification.
func (r *ReceivedNotification) UnmarshalJSON(data []byte) error {
	var a receivedAlias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	r.SubscriptionID = a.SubscriptionID
	r.ReceivedAt = a.ReceivedAt
	r.Method = a.Method
	r.Path = a.Path
	r.Header = a.Header
	r.Body = []byte(a.Body)
	return nil
}

// RestHookReceiver journals POSTs to /hook/{subscription_id} and exposes
// a control plane the orchestrator drives.
type RestHookReceiver struct {
	mu      sync.Mutex
	journal []ReceivedNotification
	cond    *sync.Cond
}

// NewRestHookReceiver returns an empty receiver.
func NewRestHookReceiver() *RestHookReceiver {
	r := &RestHookReceiver{}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Received returns a copy of the journal filtered by subscription id; "" returns all.
func (r *RestHookReceiver) Received(subscriptionID string) []ReceivedNotification {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ReceivedNotification, 0, len(r.journal))
	for _, e := range r.journal {
		if subscriptionID == "" || e.SubscriptionID == subscriptionID {
			out = append(out, e)
		}
	}
	return out
}

// Handler returns the http.Handler that serves the rest-hook + control-plane endpoints.
func (r *RestHookReceiver) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/hook/", r.handleHook)
	mux.HandleFunc("/received", r.handleGetReceivedAll)
	mux.HandleFunc("/received/", r.handleGetReceivedFiltered)
	mux.HandleFunc("/journal", r.handleJournal)
	mux.HandleFunc("/assert/notification_received", r.handleAssertNotificationReceived)

	return mux
}

func (r *RestHookReceiver) handleHook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost && req.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subID := strings.TrimPrefix(req.URL.Path, "/hook/")
	if subID == "" {
		http.Error(w, "missing subscription id", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer req.Body.Close()

	entry := ReceivedNotification{
		SubscriptionID: subID,
		ReceivedAt:     time.Now().UTC(),
		Method:         req.Method,
		Path:           req.URL.Path,
		Header:         req.Header.Clone(),
		Body:           append([]byte(nil), body...),
	}

	r.mu.Lock()
	r.journal = append(r.journal, entry)
	r.cond.Broadcast()
	r.mu.Unlock()

	w.WriteHeader(http.StatusOK)
}

func (r *RestHookReceiver) handleGetReceivedAll(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	all := r.Received("")
	writeJSON(w, http.StatusOK, all)
}

func (r *RestHookReceiver) handleGetReceivedFiltered(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subID := strings.TrimPrefix(req.URL.Path, "/received/")
	writeJSON(w, http.StatusOK, r.Received(subID))
}

func (r *RestHookReceiver) handleJournal(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.mu.Lock()
	r.journal = nil
	r.cond.Broadcast()
	r.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type assertNotificationReq struct {
	SubscriptionID string `json:"subscription_id"`
	TimeoutMs      int    `json:"timeout_ms"`
}

func (r *RestHookReceiver) handleAssertNotificationReceived(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body assertNotificationReq
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "decode: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.TimeoutMs <= 0 {
		body.TimeoutMs = 5000
	}
	deadline := time.Now().Add(time.Duration(body.TimeoutMs) * time.Millisecond)

	// Use a poll loop with the cond var so we wake on broadcast but also
	// honor the timeout precisely. We do not rely on the request's
	// context cancellation because tests deliberately need a 408
	// response, not a connection close.
	r.mu.Lock()
	for {
		matched := r.matchesLocked(body.SubscriptionID)
		if matched != nil {
			r.mu.Unlock()
			writeJSON(w, http.StatusOK, matched)
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			r.mu.Unlock()
			http.Error(w, "timeout waiting for notification", http.StatusRequestTimeout)
			return
		}
		// Sleep with the lock released; broadcast on insert wakes us up.
		r.mu.Unlock()
		t := time.NewTimer(min(remaining, 50*time.Millisecond))
		<-t.C
		r.mu.Lock()
	}
}

func (r *RestHookReceiver) matchesLocked(subID string) *ReceivedNotification {
	for i := range r.journal {
		if subID == "" || r.journal[i].SubscriptionID == subID {
			cp := r.journal[i]
			return &cp
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
