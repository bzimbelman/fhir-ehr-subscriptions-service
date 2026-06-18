// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"strings"
	"sync"
	"time"
)

// bundleHighlights is the small set of fields we surface per delivery.
// Empty values are valid — the printer renders "(unknown)" for missing
// patient / topic so the operator still sees something.
type bundleHighlights struct {
	Topic       string
	Patient     string
	EventNumber int64
}

// extractBundleHighlights pulls topic, patient ref, and event number
// out of a SubscriptionStatus + entries Bundle (the shape posted by
// internal/channel/resthook). Returns zero values for missing fields;
// returns an error only on JSON parse failure.
func extractBundleHighlights(body []byte) (bundleHighlights, error) {
	var raw struct {
		Entry []struct {
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return bundleHighlights{}, err
	}
	var h bundleHighlights
	for _, e := range raw.Entry {
		switch e.Resource["resourceType"] {
		case "SubscriptionStatus":
			if t, ok := e.Resource["topic"].(string); ok {
				h.Topic = t
			}
			if events, ok := e.Resource["notificationEvent"].([]any); ok && len(events) > 0 {
				if ev, ok := events[0].(map[string]any); ok {
					if n, ok := ev["eventNumber"].(float64); ok {
						h.EventNumber = int64(n)
					}
				}
			}
		default:
			if h.Patient == "" {
				if subj, ok := e.Resource["subject"].(map[string]any); ok {
					if ref, ok := subj["reference"].(string); ok {
						h.Patient = ref
					}
				}
			}
		}
	}
	return h, nil
}

// printer renders one line per delivery to its writer. Concurrent
// writes are serialized so two simultaneous deliveries don't tear an
// ANSI sequence in half.
type printer struct {
	mu       sync.Mutex
	w        io.Writer
	colorize bool
	now      func() time.Time
}

func newPrinter(w io.Writer, colorize bool) *printer {
	return &printer{w: w, colorize: colorize, now: time.Now}
}

// printNotification renders a single delivery line.
func (p *printer) printNotification(h bundleHighlights) {
	topic := shortTopic(h.Topic)
	if topic == "" {
		topic = "(unknown)"
	}
	patient := h.Patient
	if patient == "" {
		patient = "(unknown)"
	}
	ts := p.now().Format("15:04:05")
	line := fmt.Sprintf("[%s] notification topic=%s patient=%s event=%d",
		ts, topic, patient, h.EventNumber)
	if p.colorize {
		c := colorForTopic(h.Topic)
		line = c + line + ansiReset
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.w, line)
}

// printError emits a one-line error to the same writer; used when a
// delivery body fails to parse so the operator sees the failure.
func (p *printer) printError(format string, args ...any) {
	ts := p.now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] error: %s", ts, msg)
	if p.colorize {
		line = ansiRed + line + ansiReset
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.w, line)
}

// printInfo emits an informational line (e.g. "subscribed", "listener
// up"). Always plain (no per-topic color).
func (p *printer) printInfo(format string, args ...any) {
	ts := p.now().Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("[%s] %s", ts, msg)
	if p.colorize {
		line = ansiCyan + line + ansiReset
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintln(p.w, line)
}

// shortTopic returns the trailing path segment of a topic URL, so
// `http://demo.org/topics/lab-results` renders as `lab-results`.
func shortTopic(t string) string {
	if t == "" {
		return ""
	}
	idx := strings.LastIndex(t, "/")
	if idx < 0 || idx == len(t)-1 {
		return t
	}
	return t[idx+1:]
}

// ANSI color helpers. The palette is intentionally small (6 colors)
// so the operator can tell deliveries apart at a glance without
// needing a legend.
const (
	ansiReset = "\x1b[0m"
	ansiRed   = "\x1b[31m"
	ansiCyan  = "\x1b[36m"
)

var topicPalette = []string{
	"\x1b[32m", // green
	"\x1b[33m", // yellow
	"\x1b[34m", // blue
	"\x1b[35m", // magenta
	"\x1b[36m", // cyan
	"\x1b[91m", // bright red
}

// colorForTopic returns a stable ANSI color sequence for a topic URL.
// Same input always returns the same color.
func colorForTopic(topic string) string {
	if topic == "" {
		return topicPalette[0]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(topic))
	return topicPalette[int(h.Sum32())%len(topicPalette)]
}
