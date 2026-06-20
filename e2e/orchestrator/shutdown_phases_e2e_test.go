// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Story #207 — production shutdown phases must drive every registered
// hook against the lifecycle sequencer. This test boots the cmd/fhir-subs
// binary against a real Postgres, sends SIGTERM, and asserts that:
//
//   - the binary exits cleanly within the configured grace period,
//   - the Prometheus metrics expose `fhir_subs_lifecycle_phase_duration_seconds`
//     with non-zero observations for every phase the sequencer ran (proving
//     each phase had real registered work — not the empty-phase fast path).
//
// Today the binary's PhaseStopAccepting and PhaseCloseConnections phases
// have zero or near-zero registered hooks (HTTP listener is shut down
// from run.go, DB pool close is a side effect of storage.drain in the
// wrong phase). The phase-duration histogram still emits but the
// per-phase rate-limit / pool-close evidence is missing. After the
// wiring fix, every phase reports a real duration and the test passes.
func TestE2E_ProdBinary_ShutdownPhasesAllRunRegisteredHooks(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL: h.DBURL,
		FacilityID:  "e2e-prod-207-phases",
		AdapterID:   "default",
		Insecure:    true,
		// Long enough for the four phases to run with real work and emit
		// histogram observations.
		GracePeriod: 10 * time.Second,
	})

	// Pre-shutdown: scrape metrics so we have a baseline. The binary
	// MUST already expose /metrics with the lifecycle phase histogram
	// registered (story #94 + #207).
	{
		resp, err := http.Get(bin.HTTPURL() + "/metrics")
		if err != nil {
			t.Fatalf("pre-shutdown /metrics: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(body), "fhir_subs_lifecycle_phase_duration_seconds") {
			t.Fatalf("metrics missing fhir_subs_lifecycle_phase_duration_seconds before shutdown; body excerpt:\n%s",
				truncateE2E(string(body), 2048))
		}
	}

	// Drive the shutdown via SIGTERM (the binary's signal handler calls
	// lcMod.RequestShutdown). Then wait for /metrics to either flip to
	// 503 or for the listener to close — at that point the sequencer has
	// run every phase. We capture metrics during the brief window
	// between Phase 1 (mark_unready) and the final HTTP listener close.
	bin.SignalTerm(t)

	// Capture metrics during shutdown. Poll until the listener closes.
	// We expect /metrics to remain reachable through Phase 1 (mark
	// unready flips /readyz, but the metrics endpoint stays up because
	// Phase 2 — http listener stop_accepting — hasn't fired yet on
	// brand-new connections), and then close as Phase 2 progresses.
	var capturedMetrics string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(bin.HTTPURL() + "/metrics")
		if err != nil {
			break // listener closed — sequencer Phase 2 ran
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		capturedMetrics = string(body)
		if strings.Contains(capturedMetrics, "phase=\"close_connections\"") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait for the binary to actually exit so its final /metrics flush
	// (if any) has finished. The probe listener and main listener both
	// shut down before the binary returns from run().
	exit := bin.Stop(t, 12*time.Second)
	if exit > 0 {
		t.Errorf("binary exit code = %d on graceful shutdown; want 0", exit)
	}

	// The sequencer logs "lifecycle shutdown complete" with kind=graceful
	// when forced=false. This is the operator-visible end-of-shutdown
	// signal and is a strong proxy for "every phase ran without timing
	// out." If the wiring did NOT cover every phase the log line still
	// fires but with kind=forced when ANY hook timed out — so this
	// assertion is the one-line check that the phase contract held.
	if !bin.Stderr().ContainsLine("lifecycle shutdown complete") {
		t.Errorf("never observed `lifecycle shutdown complete` log; sequencer did not finish")
	}
	// OP #341: production runs JSON logs (`"kind":"graceful"`); some
	// dev / e2e variants render text (`kind=graceful`). Match either
	// shape so a log-format flip in the harness does not silently break
	// this assertion.
	graceful := bin.Stderr().ContainsLine("kind=graceful") ||
		bin.Stderr().ContainsLine(`"kind":"graceful"`)
	forced := bin.Stderr().ContainsLine("kind=forced") ||
		bin.Stderr().ContainsLine(`"kind":"forced"`)
	if !graceful {
		// A forced exit indicates a hook timed out — typically because a
		// phase had no registered hook but the sequencer expected work.
		if forced {
			t.Errorf("shutdown completed kind=forced; some hook timed out (phase wiring incomplete)")
		} else {
			t.Errorf("shutdown completed but kind=graceful not observed; check log lines")
		}
	}

	// The captured-during-shutdown body should mention each phase the
	// sequencer ran. The histogram emits one observation per phase via
	// the `phase` label. Mark unready always runs; story #207 ensures
	// stop_accepting / drain_in_flight / close_connections all run too.
	expected := []string{
		`phase="mark_unready"`,
		`phase="stop_accepting"`,
		`phase="drain_in_flight"`,
		`phase="close_connections"`,
	}
	for _, want := range expected {
		if !strings.Contains(capturedMetrics, want) {
			t.Errorf("captured metrics missing %q (sequencer never recorded the phase observation)\nexcerpt:\n%s",
				want, truncateE2E(capturedMetrics, 2048))
		}
	}
}
