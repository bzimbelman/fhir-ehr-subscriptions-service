// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package resthook is the demo subscriber's rest-hook receiver — the
// minimal subset of e2e/mocksub the operator-facing demo-subscriber
// needs (OP #158). Operator binaries must not import test scaffolding.
package resthook

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

// receivedAlias is the wire shape (Body as UTF-8 string).
type receivedAlias struct {
	SubscriptionID string      `json:"subscription_id"`
	ReceivedAt     time.Time   `json:"received_at"`
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	Header         http.Header `json:"header"`
	Body           string      `json:"body"`
}

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

// Receiver journals POSTs to /hook/{subscription_id} and exposes a
// control plane the demo (and ad-hoc curlers) can drive.
type Receiver struct {
	mu       sync.Mutex
	journal  []ReceivedNotification
	inserted chan struct{}
}

// NewReceiver returns an empty Receiver.
func NewReceiver() *Receiver {
	return &Receiver{
		inserted: make(chan struct{}),
	}
}

// Received returns a copy of the journal filtered by subscription id;
// "" returns all.
func (r *Receiver) Received(subscriptionID string) []ReceivedNotification {
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

// Handler returns the http.Handler for the rest-hook + control-plane
// endpoints.
func (r *Receiver) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/hook/", r.handleHook)
	mux.HandleFunc("/received", r.handleGetReceivedAll)
	mux.HandleFunc("/received/", r.handleGetReceivedFiltered)
	mux.HandleFunc("/journal", r.handleJournal)
	mux.HandleFunc("/assert/notification_received", r.handleAssertNotificationReceived)

	return mux
}

func (r *Receiver) handleHook(w http.ResponseWriter, req *http.Request) {
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
	prev := r.inserted
	r.inserted = make(chan struct{})
	r.mu.Unlock()
	close(prev)

	w.WriteHeader(http.StatusOK)
}

func (r *Receiver) handleGetReceivedAll(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, r.Received(""))
}

func (r *Receiver) handleGetReceivedFiltered(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	subID := strings.TrimPrefix(req.URL.Path, "/received/")
	writeJSON(w, http.StatusOK, r.Received(subID))
}

func (r *Receiver) handleJournal(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.mu.Lock()
	r.journal = nil
	prev := r.inserted
	r.inserted = make(chan struct{})
	r.mu.Unlock()
	close(prev)
	w.WriteHeader(http.StatusNoContent)
}

type assertNotificationReq struct {
	SubscriptionID string `json:"subscription_id"`
	TimeoutMs      int    `json:"timeout_ms"`
}

func (r *Receiver) handleAssertNotificationReceived(w http.ResponseWriter, req *http.Request) {
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
	timeout := time.NewTimer(time.Until(deadline))
	defer timeout.Stop()

	for {
		r.mu.Lock()
		matched := r.matchesLocked(body.SubscriptionID)
		signal := r.inserted
		r.mu.Unlock()

		if matched != nil {
			writeJSON(w, http.StatusOK, matched)
			return
		}
		if time.Now().After(deadline) {
			http.Error(w, "timeout waiting for notification", http.StatusRequestTimeout)
			return
		}
		select {
		case <-signal:
		case <-timeout.C:
			http.Error(w, "timeout waiting for notification", http.StatusRequestTimeout)
			return
		}
	}
}

func (r *Receiver) matchesLocked(subID string) *ReceivedNotification {
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
