// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http/httptest"
	"testing"
)

// startListener spins up an httptest.Server hosting the receiver's
// handler and registers Cleanup so it shuts down at end-of-test.
func startListener(t *testing.T, r *receiver) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)
	return srv
}
