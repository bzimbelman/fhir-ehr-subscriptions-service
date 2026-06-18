// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
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
	p := newPrinter(&sb, true /*pretty*/, false /*noColor*/)
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
	if !strings.Contains(out, "event=7") {
		t.Errorf("output missing event number: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("pretty+color on but no ANSI escape in output: %q", out)
	}
}

// TestPrinter_NotPretty_EmitsJSONLines verifies the --pretty=false
// path: each notification is a single JSON Lines record with the
// expected fields and no ANSI escapes.
func TestPrinter_NotPretty_EmitsJSONLines(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	p := newPrinter(&sb, false /*pretty*/, false /*noColor*/)
	p.printNotification(bundleHighlights{
		Topic:       "http://demo.org/topics/lab-results",
		Patient:     "Patient/ABC123",
		EventNumber: 7,
	})

	out := sb.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("JSON mode must not emit ANSI: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("expected exactly one line of JSON, got: %q", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if rec["kind"] != "notification" {
		t.Errorf(`kind: got %v, want "notification"`, rec["kind"])
	}
	if rec["label"] != "lab-results" {
		t.Errorf(`label: got %v, want "lab-results"`, rec["label"])
	}
	fields, ok := rec["fields"].(map[string]any)
	if !ok {
		t.Fatalf("fields missing/wrong type: %v", rec["fields"])
	}
	if fields["patient"] != "Patient/ABC123" {
		t.Errorf("fields.patient: got %v", fields["patient"])
	}
	if fields["event"] != "7" {
		t.Errorf("fields.event: got %v", fields["event"])
	}
}

// TestPrinter_NoColorWhenDisabled skips ANSI when noColor=true.
func TestPrinter_NoColorWhenDisabled(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	p := newPrinter(&sb, true /*pretty*/, true /*noColor*/)
	p.printNotification(bundleHighlights{
		Topic:       "http://demo.org/topics/lab-results",
		Patient:     "Patient/ABC123",
		EventNumber: 1,
	})
	if strings.Contains(sb.String(), "\x1b[") {
		t.Errorf("noColor=true but ANSI escape present: %q", sb.String())
	}
}
