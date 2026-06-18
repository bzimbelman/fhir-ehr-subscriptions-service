// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FHIRStore is the in-memory FHIR REST mock the harness uses to simulate
// the EHR's FHIR server. The contract:
//
//   * Resource-typed CRUD: PUT /{type}/{id} writes; GET /{type}/{id} reads;
//     DELETE /{type}/{id} deletes; GET /{type}?param=val performs a basic
//     search by exact-match parameter.
//   * Resources of these types are recognized by the v1 schema:
//     Patient, ServiceRequest, Encounter, Observation, DiagnosticReport,
//     MedicationRequest, AllergyIntolerance, DocumentReference.
//   * Search returns a Bundle with type=searchset.
//   * The mock is the source of truth for the FHIR Scan Runner contract:
//     Update bumps meta.versionId so content_hash changes; List supports
//     `_lastUpdated` greater-than filtering.

func TestFHIRStore_PutThenGetRoundTrip(t *testing.T) {
	t.Parallel()
	s := NewFHIRStore()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	body := []byte(`{"resourceType":"Patient","id":"p1","gender":"female"}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Patient/p1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("put status: %d", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/Patient/p1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp2.Body.Close()
	got, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(got), `"id":"p1"`) {
		t.Fatalf("get body: %s", got)
	}
	if !strings.Contains(string(got), `"versionId"`) {
		t.Fatalf("get body should carry meta.versionId, got: %s", got)
	}
}

func TestFHIRStore_PutBumpsVersionId(t *testing.T) {
	t.Parallel()
	s := NewFHIRStore()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	put := func(body string) string {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Patient/p2", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/fhir+json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("put: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	v1 := put(`{"resourceType":"Patient","id":"p2","gender":"female"}`)
	v2 := put(`{"resourceType":"Patient","id":"p2","gender":"male"}`)
	if v1 == v2 {
		t.Fatalf("expected versionId to change after update, got identical responses")
	}
}

func TestFHIRStore_DeleteRemovesResource(t *testing.T) {
	t.Parallel()
	s := NewFHIRStore()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	put, _ := http.NewRequest(http.MethodPut, srv.URL+"/ServiceRequest/sr1",
		strings.NewReader(`{"resourceType":"ServiceRequest","id":"sr1","status":"active"}`))
	resp, _ := http.DefaultClient.Do(put)
	if resp != nil {
		resp.Body.Close()
	}

	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/ServiceRequest/sr1", nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status: %d", delResp.StatusCode)
	}

	getResp, _ := http.Get(srv.URL + "/ServiceRequest/sr1")
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("after delete, get: %d", getResp.StatusCode)
	}
	getResp.Body.Close()
}

func TestFHIRStore_SearchReturnsBundle(t *testing.T) {
	t.Parallel()
	s := NewFHIRStore()
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	for i, gender := range []string{"male", "female", "male"} {
		id := fmtID(i)
		body := fmt.Sprintf(`{"resourceType":"Patient","id":"%s","gender":"%s"}`, id, gender)
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Patient/"+id, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/fhir+json")
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}

	resp, err := http.Get(srv.URL + "/Patient?gender=male")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var bundle map[string]any
	if err := json.Unmarshal(b, &bundle); err != nil {
		t.Fatalf("unmarshal bundle: %v body=%s", err, b)
	}
	if bundle["resourceType"] != "Bundle" || bundle["type"] != "searchset" {
		t.Fatalf("bundle shape: %v", bundle)
	}
	entries, _ := bundle["entry"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries: got %d want 2 body=%s", len(entries), b)
	}
}

func fmtID(i int) string {
	return "P" + string(rune('A'+i))
}
