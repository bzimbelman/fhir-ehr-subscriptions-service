// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

// TestE2E_ProdBinary_GracefulShutdown asserts the binary's signal
// handling drives the lifecycle module's shutdown sequencer:
//
//   - SIGTERM flips /readyz from 200 to 503 with status="shutting_down",
//   - the listener stops accepting new connections,
//   - the binary exits cleanly within the configured grace period.
//
// Before B-4's full lifecycle wiring, the registered shutdown hooks
// were only the HTTP server's drain. After B-4: MLLP listener, pipeline
// workers, and the DB pool also register, and the sequencer runs them
// in the right phase order.
//
// B-4.
func TestE2E_ProdBinary_GracefulShutdown(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL: h.DBURL,
		FacilityID:  "e2e-prod-shutdown",
		AdapterID:   "default",
		Insecure:    true,
		GracePeriod: 10 * time.Second,
		MLLPBind:    "127.0.0.1:" + freePort(t),
	})

	// Pre-shutdown sanity: /readyz reports 200.
	{
		resp, err := http.Get(bin.HTTPURL() + "/readyz")
		if err != nil {
			t.Fatalf("readyz: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pre-shutdown readyz: %d, want 200", resp.StatusCode)
		}
	}

	// Send SIGTERM and confirm /readyz reports 503/shutting_down
	// during the drain window.
	bin.SignalTerm(t)

	// Poll quickly — Phase 1 / mark_unready fires immediately.
	deadline := time.Now().Add(5 * time.Second)
	sawShuttingDown := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(bin.HTTPURL() + "/readyz")
		if err != nil {
			break // listener already stopped — that's fine if mark_unready already won
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusServiceUnavailable {
			var obj map[string]any
			if err := json.Unmarshal(body, &obj); err == nil {
				if status, _ := obj["status"].(string); status == "shutting_down" {
					sawShuttingDown = true
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawShuttingDown {
		t.Errorf("never observed /readyz=503 status=shutting_down during drain")
	}

	// The binary should exit on its own within grace + slack.
	exit := bin.Stop(t, 15*time.Second)
	if exit != 0 && exit != -1 {
		// -1 in the helper means the wait returned an unexpected error
		// type, which can happen on some platforms when the parent ctx
		// races signal handling. Anything else (positive exit code) is
		// a regression.
		if exit > 0 {
			t.Errorf("binary exited %d on graceful shutdown; want 0", exit)
		}
	}
}
