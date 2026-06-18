// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import "fmt"

// errSink is the subset of *testing.T the docker gate uses.
type errSink interface {
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
	Helper()
}

// dockerGate is the policy point for "what to do when testcontainers
// can't reach Docker."
//
//   - err == nil — no-op.
//   - allowNoDocker == false (default; what CI does) — t.Fatalf with
//     the underlying error and a hint pointing at E2E_ALLOW_NO_DOCKER.
//   - allowNoDocker == true (local dev opt-in via E2E_ALLOW_NO_DOCKER=1)
//     — t.Skipf with the underlying error.
//
// CI never sets E2E_ALLOW_NO_DOCKER. A CI environment with broken Docker
// therefore reports red, not soft-green. Local devs without Docker can
// run `E2E_ALLOW_NO_DOCKER=1 make e2e` to get the same skip behavior the
// soft-skip default used to provide.
func dockerGate(t errSink, err error, allowNoDocker bool) {
	t.Helper()
	if err == nil {
		return
	}
	if allowNoDocker {
		t.Skipf("docker required but unavailable; E2E_ALLOW_NO_DOCKER=1 honored: %v", err)
		return
	}
	t.Fatalf("docker required but unavailable; set E2E_ALLOW_NO_DOCKER=1 to skip locally: %v",
		errOrString(err))
}

// errOrString narrows error to its message so the test's substring match
// against `t.Fatalf` output works without depending on the test's idea
// of how `%v` renders an error wrapper.
func errOrString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%v", err)
}
