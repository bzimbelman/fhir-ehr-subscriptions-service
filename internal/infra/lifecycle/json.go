// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"encoding/json"
	"net/http"
)

// writeJSONStatus serializes the standard probe response shape and writes
// it to w with the given HTTP status code.
//
//	{"status":"<status>"}                     // optional failed list omitted
//	{"status":"<status>","failed":[...]}      // when failed is non-nil
//
// LLD §8 caps the response at 4 KiB; the bodies emitted here are tiny
// (single-byte status strings + at most a small list of names), so the cap
// is enforced by structure rather than truncation.
func writeJSONStatus(w http.ResponseWriter, code int, status string, failed []string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body := probeResponse{Status: status, Failed: failed}
	// Errors here are the connection going away; the response code is
	// already on the wire so the caller has the answer they need.
	_ = json.NewEncoder(w).Encode(body)
}

// probeResponse is the wire shape for `/healthz`, `/readyz`, and
// `/startup` responses.
type probeResponse struct {
	Status string   `json:"status"`
	Failed []string `json:"failed,omitempty"`
}
