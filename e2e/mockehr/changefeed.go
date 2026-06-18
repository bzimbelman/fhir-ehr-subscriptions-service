// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// ChangeFeedEvent is one record published over the change-feed stream.
type ChangeFeedEvent struct {
	ID           string `json:"id"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ChangeKind   string `json:"change_kind"`
	Version      int    `json:"version"`
}

// ChangeFeed is the EHR-side mock of a vendor change-feed API. It buffers
// events published before a subscriber connects (so test setup order
// doesn't matter) and broadcasts to all current subscribers.
type ChangeFeed struct {
	mu          sync.Mutex
	buffer      []ChangeFeedEvent
	subscribers map[chan ChangeFeedEvent]struct{}
}

// NewChangeFeed returns an empty change feed.
func NewChangeFeed() *ChangeFeed {
	return &ChangeFeed{
		subscribers: map[chan ChangeFeedEvent]struct{}{},
	}
}

// Publish records an event. If subscribers are connected, the event is
// fanned out; if not, it is buffered for the next connect.
func (c *ChangeFeed) Publish(e ChangeFeedEvent) {
	c.mu.Lock()
	c.buffer = append(c.buffer, e)
	for ch := range c.subscribers {
		select {
		case ch <- e:
		default: // drop on slow subscriber; mirrors vendor SSE semantics
		}
	}
	c.mu.Unlock()
}

// Handler returns the SSE handler at /change-feed.
func (c *ChangeFeed) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/change-feed", c.serveSSE)
	return mux
}

func (c *ChangeFeed) serveSSE(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan ChangeFeedEvent, 64)
	c.mu.Lock()
	// Flush the buffer to this subscriber first so events published
	// before connect are visible.
	buffered := append([]ChangeFeedEvent(nil), c.buffer...)
	c.subscribers[ch] = struct{}{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.subscribers, ch)
		c.mu.Unlock()
	}()

	for _, e := range buffered {
		if err := writeSSE(w, e); err != nil {
			return
		}
	}
	flusher.Flush()

	for {
		select {
		case <-req.Context().Done():
			return
		case e := <-ch:
			if err := writeSSE(w, e); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e ChangeFeedEvent) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
}
