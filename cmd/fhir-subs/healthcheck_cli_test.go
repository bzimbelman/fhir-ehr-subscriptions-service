// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OP #230: realMain's "healthcheck" first-arg dispatch routes to the
// healthcheck subcommand handler. The subcommand performs an HTTP GET
// against a probe URL and exits 0 on 2xx, 1 on non-2xx, 1 on dial
// error.
func TestRealMain_HealthcheckOK(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"healthcheck", "--url", srv.URL + "/readyz"}, stdout, stderr)
	if rc != 0 {
		t.Errorf("rc: want 0, got %d (stderr=%q)", rc, stderr.String())
	}
}

// OP #230: a non-2xx response from the probe URL exits 1 so docker's
// healthcheck reports "unhealthy". The diagnostic line on stderr names
// the URL and the status code so an operator inspecting `docker inspect`
// can see what failed.
func TestRealMain_HealthcheckNon2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"healthcheck", "--url", srv.URL + "/readyz"}, stdout, stderr)
	if rc != 1 {
		t.Errorf("rc: want 1, got %d (stderr=%q)", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "503") {
		t.Errorf("stderr missing status code: %q", stderr.String())
	}
}

// OP #230: a dial error (no listener) exits 1 with a diagnostic that
// includes the URL.
func TestRealMain_HealthcheckDialError(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	// 127.0.0.1:1 is reserved-ish and almost never has a listener.
	rc := realMain([]string{"healthcheck", "--url", "http://127.0.0.1:1/readyz"}, stdout, stderr)
	if rc != 1 {
		t.Errorf("rc: want 1, got %d", rc)
	}
	if !strings.Contains(stderr.String(), "127.0.0.1:1") {
		t.Errorf("stderr missing URL: %q", stderr.String())
	}
}

// OP #230: --help prints usage to stderr and exits 0.
func TestRealMain_HealthcheckHelp(t *testing.T) {
	t.Parallel()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"healthcheck", "--help"}, stdout, stderr)
	if rc != 0 {
		t.Errorf("rc: want 0, got %d", rc)
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr missing usage line: %q", stderr.String())
	}
}

// OP #230: with no flags, the subcommand defaults to
// http://127.0.0.1:8081/readyz (the bridge's default ProbeBind +
// /readyz). This is the call the demo compose healthcheck makes.
func TestRealMain_HealthcheckDefaultURL(t *testing.T) {
	t.Parallel()
	// We cannot actually bind 127.0.0.1:8081 in a parallel unit test
	// without colliding with a developer's running bridge; instead we
	// assert that with no --url, a dial-error to the default URL is
	// produced (which proves the default was used).
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	rc := realMain([]string{"healthcheck", "--timeout", "100ms"}, stdout, stderr)
	if rc == 0 {
		t.Skip("default URL happens to be live (8081 listener present); skipping")
	}
	if !strings.Contains(stderr.String(), "127.0.0.1:8081") && !strings.Contains(stderr.String(), "/readyz") {
		t.Errorf("stderr does not show the default URL was used: %q", stderr.String())
	}
}
