// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Tests covering the SHOULD-FIX S-2 set in the production-readiness
// audit. Each test pins one of the audit's specific defects.
package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// S-2.10: $events with a malformed eventsSinceNumber must return 400,
// not silently treat the unparseable value as 0.
func TestEvents_InvalidSinceParam_400(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)

	cases := []string{
		"eventsSinceNumber=abc",
		"eventsSinceNumber=-1",
		"eventsUntilNumber=foo",
	}
	for _, q := range cases {
		q := q
		t.Run(q, func(t *testing.T) {
			t.Parallel()
			resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events?" + q)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, body)
			}
		})
	}
}

// S-2.13: CapabilityStatement.fhirVersion must come from a
// configurable Deps.FHIRVersion, not be hardcoded "5.0.0".
func TestMetadata_FHIRVersionConfigurable(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.FHIRVersion = "4.0.1"
	srv := newTestServer(t, defaultPrincipal(), deps)

	resp, err := http.Get(srv.URL + "/metadata")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var cs map[string]any
	if err := json.Unmarshal(body, &cs); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if cs["fhirVersion"] != "4.0.1" {
		t.Fatalf("fhirVersion = %v; want 4.0.1", cs["fhirVersion"])
	}
}

// S-2.9: timestamps must use the FHIR `instant` Z form ("Z"), not
// "+00:00", to satisfy implementations that parse the instant type
// strictly.
func TestEvents_TimestampUsesZForm(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)

	resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "+00:00") {
		t.Fatalf("instant must use Z form, not +00:00: %s", body)
	}
	if !strings.Contains(string(body), `"timestamp"`) {
		t.Fatalf("expected timestamp field: %s", body)
	}
}

// S-2.11: GET /Subscription/$status must cap the count of `id`
// parameters to defend the API from an attacker pinning the DB pool.
func TestStatusBulk_TooManyIDsRejected(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	parts := make([]string, 0, 257)
	for i := 0; i < 257; i++ {
		parts = append(parts, "id="+fmt.Sprintf("%d-0000-0000-0000-000000000000", i+1000))
	}
	resp, err := http.Get(srv.URL + "/Subscription/$status?" + strings.Join(parts, "&"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, body)
	}
}

// S-2.6: ETag must be a version weak-tag (W/"<version>") not the
// resource id; If-Match with an unquoted UUID must NOT be accepted as
// a match (lost-update cannot be detected if id-as-version).
func TestUpdate_IfMatch_RejectsUnquotedID(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	// Unquoted id form — not a valid weak ETag.
	req.Header.Set("If-Match", id.String())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d; want 409; body=%s", resp.StatusCode, respBody)
	}
}

// S-2.3: schema-validation errors must be capped at a fixed length so
// pathological JSON cannot blow the response body up.
func TestCreate_SchemaError_Capped(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	// 100 KiB of nonsense fields — most will fail validation; the
	// resulting error message should still fit in a small bound.
	pathological := `{"resourceType":"Subscription","status":"requested","junk":"` + strings.Repeat("x", 100*1024) + `"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(pathological))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400; body=%s", resp.StatusCode, respBody)
	}
	if len(respBody) > 4096 {
		t.Fatalf("validation error body too large (%d bytes); should be capped", len(respBody))
	}
}

// S-2.2: body size limit must be configurable via Deps.MaxBodyBytes.
func TestCreate_BodySizeConfigurable(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.MaxBodyBytes = 512 // very small cap
	srv := newTestServer(t, defaultPrincipal(), deps)

	// Body just under cap — should be processed (then fail schema).
	small := `{"resourceType":"Subscription","status":"requested"}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(small))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	// Will be 400 (schema), confirming the read succeeded.
	if resp.StatusCode != http.StatusBadRequest {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("small body status = %d; want 400; body=%s", resp.StatusCode, respBody)
	}

	// Body well above cap — must be rejected with 413.
	big := `{"resourceType":"Subscription","junk":"` + strings.Repeat("a", 1024) + `"}`
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("big body status = %d; want 413; body=%s", resp.StatusCode, respBody)
	}
}
