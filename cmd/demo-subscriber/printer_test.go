// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// TestExtractBundleHighlights pulls patient ref, topic URL, and event
// number out of a SubscriptionStatus + entries Bundle (the shape the
// rest-hook channel posts).
func TestExtractBundleHighlights(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"resourceType": "Bundle",
		"type": "subscription-notification",
		"entry": [
			{
				"resource": {
					"resourceType": "SubscriptionStatus",
					"type": "event-notification",
					"topic": "http://demo.org/topics/lab-results",
					"subscription": {"reference": "Subscription/abc"},
					"notificationEvent": [
						{"eventNumber": 7, "focus": {"reference": "Observation/obs1"}}
					]
				}
			},
			{
				"resource": {
					"resourceType": "Observation",
					"id": "obs1",
					"subject": {"reference": "Patient/ABC123"}
				}
			}
		]
	}`)

	got, err := extractBundleHighlights(body)
	if err != nil {
		t.Fatalf("extractBundleHighlights: %v", err)
	}
	if got.Topic != "http://demo.org/topics/lab-results" {
		t.Errorf("Topic: got %q want lab-results url", got.Topic)
	}
	if got.Patient != "Patient/ABC123" {
		t.Errorf("Patient: got %q want Patient/ABC123", got.Patient)
	}
	if got.EventNumber != 7 {
		t.Errorf("EventNumber: got %d want 7", got.EventNumber)
	}
}

// TestExtractBundleHighlights_MissingFields returns zero-values rather
// than failing — the demo printer must keep going on a malformed
// notification so the operator sees something useful.
func TestExtractBundleHighlights_MissingFields(t *testing.T) {
	t.Parallel()

	got, err := extractBundleHighlights([]byte(`{"resourceType":"Bundle","type":"subscription-notification","entry":[]}`))
	if err != nil {
		t.Fatalf("extractBundleHighlights: %v", err)
	}
	if got.Topic != "" || got.Patient != "" || got.EventNumber != 0 {
		t.Errorf("unexpected non-zero highlights: %+v", got)
	}
}

// TestExtractBundleHighlights_BadJSON returns an error so the caller
// can flag the malformed delivery.
func TestExtractBundleHighlights_BadJSON(t *testing.T) {
	t.Parallel()

	if _, err := extractBundleHighlights([]byte(`not-json`)); err == nil {
		t.Fatal("want error on bad JSON; got nil")
	}
}

// TestPrinter_PrintsHighlights formats a notification line that
// includes patient, topic (short form), and event number. Color codes
// are present when colorize=true.
func TestPrinter_PrintsHighlights(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	p := newPrinter(&sb, true)
	p.printNotification(bundleHighlights{
		Topic:       "http://demo.org/topics/lab-results",
		Patient:     "Patient/ABC123",
		EventNumber: 7,
	})

	out := sb.String()
	if !strings.Contains(out, "Patient/ABC123") {
		t.Errorf("output missing patient: %q", out)
	}
	if !strings.Contains(out, "lab-results") {
		t.Errorf("output missing topic short form: %q", out)
	}
	if !strings.Contains(out, "7") {
		t.Errorf("output missing event number: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("colorize=true but no ANSI escape in output: %q", out)
	}
}

// TestPrinter_NoColorWhenDisabled skips ANSI when colorize=false.
func TestPrinter_NoColorWhenDisabled(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	p := newPrinter(&sb, false)
	p.printNotification(bundleHighlights{
		Topic:       "http://demo.org/topics/lab-results",
		Patient:     "Patient/ABC123",
		EventNumber: 1,
	})
	if strings.Contains(sb.String(), "\x1b[") {
		t.Errorf("colorize=false but ANSI escape present: %q", sb.String())
	}
}

// TestColorForTopic_Stable returns the same color for the same topic
// across calls (so the operator sees consistent stripes).
func TestColorForTopic_Stable(t *testing.T) {
	t.Parallel()

	a := colorForTopic("http://demo.org/topics/lab-results")
	b := colorForTopic("http://demo.org/topics/lab-results")
	if a != b {
		t.Errorf("colorForTopic not stable: %q vs %q", a, b)
	}
	c := colorForTopic("http://demo.org/topics/encounters")
	if c == a {
		// Not strictly required, but the palette has 6 colors so
		// this is overwhelmingly likely.
		t.Logf("warning: two topics hashed to the same color (%q)", a)
	}
}
