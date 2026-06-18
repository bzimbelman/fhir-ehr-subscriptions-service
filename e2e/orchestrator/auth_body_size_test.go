// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"net/http"
	"strings"
	"testing"
)

// TestE2E_TokenEndpoint_RejectsLargeBody POSTs a 10 MiB body at a real
// /token instance and asserts the server returns 413 Request Entity
// Too Large without OOMing or hanging. Regression guard for B-6.
func TestE2E_TokenEndpoint_RejectsLargeBody(t *testing.T) {
	t.Parallel()

	clientID := "body-size-client"
	jwksSrv, _, _ := newJWKSServer(t)
	_, srv := newTokenEndpointE2E(t, clientID, jwksSrv.URL+"/jwks")

	const bodySize = 10 * 1024 * 1024
	body := "grant_type=client_credentials" +
		"&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer" +
		"&client_assertion=" + strings.Repeat("x", bodySize)

	resp, err := http.Post(srv.URL, "application/x-www-form-urlencoded", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", resp.StatusCode)
	}
}
