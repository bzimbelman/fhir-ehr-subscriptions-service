// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/cliprint"
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

// printer renders one line per delivery through a cliprint.Formatter.
// The formatter handles color, emoji, and pretty-vs-JSON-Lines mode
// selection; the printer just builds the Event.
type printer struct {
	fmt *cliprint.Formatter
	now func() time.Time
}

func newPrinter(w io.Writer, pretty, noColor bool) *printer {
	return &printer{
		fmt: cliprint.NewFormatter(w, cliprint.Options{Pretty: pretty, NoColor: noColor}),
		now: time.Now,
	}
}

// printNotification renders a single delivery as a structured event.
func (p *printer) printNotification(h bundleHighlights) {
	topic := shortTopic(h.Topic)
	if topic == "" {
		topic = "(unknown)"
	}
	patient := h.Patient
	if patient == "" {
		patient = "(unknown)"
	}
	p.fmt.Emit(cliprint.Event{
		Time:   p.now(),
		Kind:   cliprint.KindNotification,
		Status: cliprint.StatusOK,
		Label:  topic,
		Fields: []cliprint.Field{
			{K: "patient", V: patient},
			{K: "event", V: fmt.Sprintf("%d", h.EventNumber)},
		},
	})
}

// printError emits a one-line error event; used when a delivery body
// fails to parse so the operator sees the failure.
func (p *printer) printError(format string, args ...any) {
	p.fmt.Emit(cliprint.Event{
		Time:   p.now(),
		Kind:   cliprint.KindError,
		Status: cliprint.StatusFail,
		Msg:    fmt.Sprintf(format, args...),
	})
}

// printInfo emits an informational event (e.g. "subscribed", "listener
// up").
func (p *printer) printInfo(format string, args ...any) {
	p.fmt.Emit(cliprint.Event{
		Time:   p.now(),
		Kind:   cliprint.KindInfo,
		Status: cliprint.StatusInfo,
		Msg:    fmt.Sprintf(format, args...),
	})
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
