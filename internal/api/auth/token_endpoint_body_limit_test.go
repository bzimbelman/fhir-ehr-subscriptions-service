// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTokenEndpoint_OversizedBody_Rejected ensures /token rejects request
// bodies larger than the configured maximum without consuming all of
// them in memory. The unauthenticated nature of /token means an attacker
// can flood multi-megabyte POSTs to OOM the process unless ParseForm is
// gated by http.MaxBytesReader (B-6).
func TestTokenEndpoint_OversizedBody_Rejected(t *testing.T) {
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)

	// Build a 10MiB form body — well past the 64KiB default cap.
	const bodySize = 10 * 1024 * 1024
	pad := strings.Repeat("x", bodySize)
	body := "grant_type=client_credentials&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer&client_assertion=" + pad

	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	te.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413 (Request Entity Too Large)", rec.Code)
	}
	// FHIR-shaped error response, no panic, no full-body echo.
	if !strings.Contains(rec.Body.String(), "OperationOutcome") {
		t.Errorf("expected OperationOutcome; body=%s", rec.Body.String()[:min(200, len(rec.Body.String()))])
	}
	// Diagnostics MUST NOT echo the giant body. The body length cap is
	// what we're proving — assert the response body itself is small.
	if rec.Body.Len() > 4096 {
		t.Errorf("response body too large (%d bytes) — likely echoed input", rec.Body.Len())
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
