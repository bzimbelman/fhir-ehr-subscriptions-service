// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package versionshim_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/versionshim"
)

// P2.4: Top-level R5 fields project onto the R4B Backport shape:
//   - topic → criteria
//   - channelType.code → channel.type
//   - endpoint → channel.endpoint
//   - content → channel.payload
//   - heartbeatPeriod, timeout → channel.heartbeatPeriod, channel.timeout
func TestRenderSubscriptionR4B_TopLevelMapping(t *testing.T) {
	t.Parallel()
	r5 := []byte(`{
		"resourceType": "Subscription",
		"id": "abc-123",
		"status": "active",
		"topic": "http://example.org/topics/order-changed",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://sub.example.org/notif",
		"content": "id-only",
		"heartbeatPeriod": 60,
		"timeout": 30
	}`)
	got, err := versionshim.RenderSubscriptionR4B(r5)
	if err != nil {
		t.Fatalf("RenderSubscriptionR4B: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc["resourceType"] != "Subscription" {
		t.Errorf("resourceType: %v", doc["resourceType"])
	}
	if doc["id"] != "abc-123" {
		t.Errorf("id: %v", doc["id"])
	}
	if doc["status"] != "active" {
		t.Errorf("status: %v", doc["status"])
	}
	if doc["criteria"] != "http://example.org/topics/order-changed" {
		t.Errorf("criteria: %v", doc["criteria"])
	}
	if _, ok := doc["topic"]; ok {
		t.Errorf("topic must not appear at top-level in R4B form: %#v", doc["topic"])
	}
	channel, ok := doc["channel"].(map[string]any)
	if !ok {
		t.Fatalf("channel missing or wrong type: %#v", doc["channel"])
	}
	if channel["type"] != "rest-hook" {
		t.Errorf("channel.type: %v", channel["type"])
	}
	if channel["endpoint"] != "https://sub.example.org/notif" {
		t.Errorf("channel.endpoint: %v", channel["endpoint"])
	}
	if channel["payload"] != "id-only" {
		t.Errorf("channel.payload: %v", channel["payload"])
	}
	if channel["heartbeatPeriod"] != float64(60) {
		t.Errorf("channel.heartbeatPeriod: %v", channel["heartbeatPeriod"])
	}
	if channel["timeout"] != float64(30) {
		t.Errorf("channel.timeout: %v", channel["timeout"])
	}
}

// P2.4: R5 header (array of {name,value} objects) flattens to R4B's
// "Name: Value" string form.
func TestRenderSubscriptionR4B_HeaderFlattens(t *testing.T) {
	t.Parallel()
	r5 := []byte(`{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/t",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://x",
		"header": [
			{"name": "Authorization", "value": "Bearer abc"},
			{"name": "X-Tenant", "value": "alpha"}
		]
	}`)
	got, err := versionshim.RenderSubscriptionR4B(r5)
	if err != nil {
		t.Fatalf("RenderSubscriptionR4B: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(got, &doc)
	channel := doc["channel"].(map[string]any)
	hdrs, ok := channel["header"].([]any)
	if !ok {
		t.Fatalf("channel.header missing or wrong type: %#v", channel["header"])
	}
	want := []string{"Authorization: Bearer abc", "X-Tenant: alpha"}
	if len(hdrs) != len(want) {
		t.Fatalf("header count: want %d, got %d", len(want), len(hdrs))
	}
	for i, w := range want {
		if hdrs[i] != w {
			t.Errorf("header[%d]: want %q, got %q", i, w, hdrs[i])
		}
	}
}

// P2.4: filterBy lifts onto _criteria.extension as the Backport
// criteria-filter extension, with valueString = "<param>[:<modifier>][<comparator>]=<value>".
func TestRenderSubscriptionR4B_FilterByLifts(t *testing.T) {
	t.Parallel()
	r5 := []byte(`{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/t",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://x",
		"filterBy": [
			{"resourceType":"ServiceRequest","filterParameter":"patient","value":"Patient/123"},
			{"resourceType":"ServiceRequest","filterParameter":"status","modifier":"not","value":"draft"}
		]
	}`)
	got, err := versionshim.RenderSubscriptionR4B(r5)
	if err != nil {
		t.Fatalf("RenderSubscriptionR4B: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal(got, &doc)
	if _, ok := doc["filterBy"]; ok {
		t.Errorf("filterBy must not survive on the R4B wire: %#v", doc["filterBy"])
	}
	crit, ok := doc["_criteria"].(map[string]any)
	if !ok {
		t.Fatalf("_criteria missing or wrong type: %#v", doc["_criteria"])
	}
	exts, ok := crit["extension"].([]any)
	if !ok || len(exts) != 2 {
		t.Fatalf("_criteria.extension wrong: %#v", crit["extension"])
	}
	first := exts[0].(map[string]any)
	if !strings.Contains(first["url"].(string), "backport-filter-criteria") {
		t.Errorf("first ext url: %v", first["url"])
	}
	if first["valueString"] != "patient=Patient/123" {
		t.Errorf("first ext valueString: %v", first["valueString"])
	}
	second := exts[1].(map[string]any)
	if second["valueString"] != "status:not=draft" {
		t.Errorf("second ext valueString: %v", second["valueString"])
	}
}

// P2.4: A non-Subscription resource is passed through unchanged so a
// stray call against a Patient body does not corrupt it.
func TestRenderSubscriptionR4B_NonSubscriptionPassthrough(t *testing.T) {
	t.Parallel()
	patient := []byte(`{"resourceType":"Patient","id":"p1"}`)
	got, err := versionshim.RenderSubscriptionR4B(patient)
	if err != nil {
		t.Fatalf("RenderSubscriptionR4B: %v", err)
	}
	if string(got) != string(patient) {
		t.Errorf("non-subscription must pass through unchanged.\nwant: %s\ngot:  %s", patient, got)
	}
}

// P2.4: Malformed JSON returns an error.
func TestRenderSubscriptionR4B_MalformedJSONErrors(t *testing.T) {
	t.Parallel()
	if _, err := versionshim.RenderSubscriptionR4B([]byte("not json")); err == nil {
		t.Errorf("expected error on malformed JSON")
	}
}
