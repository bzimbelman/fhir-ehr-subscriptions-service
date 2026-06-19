// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Package demo_test drives the documented demo happy path end-to-end via
// `docker compose up` against demo/docker-compose.yml, the real
// demo-publisher CLI, and the real demo-subscriber CLI. It is the
// regression seal for OpenProject #154 — three compounding bugs broke
// the README walkthrough so zero notifications were delivered.
//
// AC #5 (OP #154): an e2e/demo/demo_walkthrough_test.go MUST script the
// README's commands and assert a notification arrives.
package demo_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repoRoot returns the repo root, derived from this file's path. Tests
// run from the package directory, so we walk two levels up
// (e2e/demo/ -> e2e/ -> repo root).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// composeFile returns the absolute path to demo/docker-compose.yml.
func composeFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "demo", "docker-compose.yml")
}

// dockerAvailable returns nil if `docker` and `docker compose version`
// both succeed.
func dockerAvailable() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker binary not on PATH: %w", err)
	}
	if out, err := exec.Command("docker", "compose", "version").CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose version: %v: %s", err, string(out))
	}
	return nil
}

// projectName returns the docker-compose project name used by this
// test. Each run gets its own name so parallel runs (or stale
// containers from a prior failure) cannot collide.
func projectName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("fhirsubs-demo-%d", time.Now().UnixNano())
}

// composeCmd builds an exec.Cmd targeting demo/docker-compose.yml under
// a unique project name. Errors during creation surface as t.Fatalf.
func composeCmd(t *testing.T, project string, args ...string) *exec.Cmd {
	t.Helper()
	full := []string{"compose", "-p", project, "-f", composeFile(t)}
	full = append(full, args...)
	cmd := exec.Command("docker", full...)
	cmd.Env = append(os.Environ(), "COMPOSE_PROJECT_NAME="+project)
	return cmd
}

// composeRun runs `docker compose ...` and t.Fatalfs on failure.
func composeRun(t *testing.T, project string, args ...string) string {
	t.Helper()
	cmd := composeCmd(t, project, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose %s failed: %v\noutput:\n%s",
			strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

// composeDownAll tears the stack down with -v so the next run starts
// from a clean Postgres.
func composeDownAll(t *testing.T, project string) {
	t.Helper()
	cmd := composeCmd(t, project, "down", "-v", "--remove-orphans")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Best-effort cleanup; report but do not fail the test.
		t.Logf("compose down: %v\n%s", err, string(out))
	}
}

// waitForHTTP polls url until it returns 2xx or deadline elapses.
func waitForHTTP(url string, deadline time.Time) error {
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", url)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// fetchJournal GETs /journal off the demo-subscriber and decodes it.
// The endpoint is part of the bundled mocksub.RestHookReceiver and
// returns the list of POSTs the subscriber has received from the
// bridge.
func fetchJournal(url string) ([]map[string]any, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("journal GET %s: %d: %s", url, resp.StatusCode, string(body))
	}
	var entries []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// TestDemoWalkthroughDeliversNotifications drives the documented happy
// path: bring up the demo compose stack (bridge + postgres +
// demo-subscriber), run the demo-publisher's bundled scenario catalog,
// and assert the subscriber received at least 2 notification Bundles
// (the two `oru-r01 patient=ABC123` entries in scenarios/labs.yaml).
//
// This test is intentionally slow (real `docker compose up --build`).
// It is the seal that the README walkthrough actually delivers.
//
// Skip policy: if Docker is not available AND E2E_ALLOW_NO_DOCKER=1 is
// set, the test t.Skips. Otherwise it t.Fatalfs — CI must hit Docker.
func TestDemoWalkthroughDeliversNotifications(t *testing.T) {
	if testing.Short() {
		t.Skip("docker-compose walkthrough is slow; skipped in -short mode")
	}
	allowNoDocker := os.Getenv("E2E_ALLOW_NO_DOCKER") == "1"
	if err := dockerAvailable(); err != nil {
		if allowNoDocker {
			t.Skipf("docker unavailable; E2E_ALLOW_NO_DOCKER=1 honored: %v", err)
		}
		t.Fatalf("docker required but unavailable; set E2E_ALLOW_NO_DOCKER=1 to skip locally: %v", err)
	}

	project := projectName(t)
	t.Cleanup(func() { composeDownAll(t, project) })

	// 1. Build + bring up the stack. --build ensures the Dockerfile
	//    we're testing is the one that runs (AC #3: Dockerfile MUST
	//    build all three binaries).
	composeRun(t, project, "up", "-d", "--build", "--wait", "--wait-timeout", "180")

	// 2. Wait for bridge readiness via the published port from the
	//    compose mapping (host:8443 -> bridge:8443).
	bridgeURL := "http://localhost:8443"
	deadline := time.Now().Add(60 * time.Second)
	if err := waitForHTTP(bridgeURL+"/readyz", deadline); err != nil {
		// Dump bridge logs to make CI failures debuggable.
		logs, _ := composeCmd(t, project, "logs", "bridge").CombinedOutput()
		t.Fatalf("bridge /readyz never became healthy: %v\nbridge logs:\n%s", err, string(logs))
	}

	// 3. Wait for the subscriber's `/journal` endpoint to come up.
	//    The compose stack publishes demo-subscriber on host port 9090.
	subscriberURL := "http://localhost:9090"
	if err := waitForHTTP(subscriberURL+"/journal", time.Now().Add(60*time.Second)); err != nil {
		logs, _ := composeCmd(t, project, "logs", "demo-subscriber").CombinedOutput()
		t.Fatalf("demo-subscriber /journal never became reachable: %v\nlogs:\n%s",
			err, string(logs))
	}

	// 4. Run the publisher against the bridge's MLLP listener via a
	//    one-shot compose `run`. The publisher binary must be in the
	//    image; AC #3 covers the Dockerfile change.
	pubOut, pubErr := composeCmd(t, project, "run", "--rm", "demo-publisher").CombinedOutput()
	if pubErr != nil {
		t.Fatalf("demo-publisher run failed: %v\noutput:\n%s", pubErr, string(pubOut))
	}
	t.Logf("publisher output:\n%s", string(pubOut))

	// 5. Poll the subscriber's journal until at least 2 notifications
	//    arrive (the two ABC123 lab-results entries).  Notifications
	//    are asynchronous: MLLP ack -> hl7processor -> matcher ->
	//    submatcher -> builder -> scheduler -> rest-hook POST.
	deadline = time.Now().Add(60 * time.Second)
	var lastJournal []map[string]any
	for {
		entries, err := fetchJournal(subscriberURL + "/journal")
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("journal fetch failed: %v", err)
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		lastJournal = entries
		// We expect 2 ABC123 notifications. The bridge may also send
		// one handshake bundle (Subscription activation) — that
		// counts as a journal entry but its body shape is different.
		// We require at least 2 NON-handshake entries.
		nonHandshake := 0
		for _, e := range entries {
			if isNotification(e) {
				nonHandshake++
			}
		}
		if nonHandshake >= 2 {
			break
		}
		if time.Now().After(deadline) {
			bridgeLogs, _ := composeCmd(t, project, "logs", "bridge").CombinedOutput()
			subLogs, _ := composeCmd(t, project, "logs", "demo-subscriber").CombinedOutput()
			t.Fatalf("expected >=2 notifications, journal has %d non-handshake (full=%d)\nbridge logs:\n%s\nsubscriber logs:\n%s",
				nonHandshake, len(entries), string(bridgeLogs), string(subLogs))
		}
		time.Sleep(1 * time.Second)
	}

	t.Logf("delivered %d journal entries", len(lastJournal))
}

// isNotification returns true when the journaled body looks like a
// SubscriptionStatus notification Bundle (vs. a handshake). The bridge
// activates the subscription with a synthetic handshake Bundle whose
// type=="history" and which contains zero notificationEvent entries.
func isNotification(entry map[string]any) bool {
	body, ok := entry["body"].(map[string]any)
	if !ok {
		return false
	}
	entries, ok := body["entry"].([]any)
	if !ok || len(entries) == 0 {
		return false
	}
	first, ok := entries[0].(map[string]any)
	if !ok {
		return false
	}
	resource, ok := first["resource"].(map[string]any)
	if !ok {
		return false
	}
	if resource["resourceType"] != "SubscriptionStatus" {
		return false
	}
	// notificationEvent[] is non-empty only on actual notifications.
	ne, ok := resource["notificationEvent"].([]any)
	if !ok {
		return false
	}
	return len(ne) > 0
}
