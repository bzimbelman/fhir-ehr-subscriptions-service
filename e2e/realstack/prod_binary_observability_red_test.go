// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// OpenProject story #237 — End-to-end observability coverage for the
// prod binary running inside the realstack:
//
//  1. /metrics is reachable on the bridge's primary HTTP listener,
//     speaks Prometheus text format, and exposes the named counters
//     internal/api/metrics declares.
//  2. The binary's OTel exporter actually pushes spans into the
//     collector — at least one span lands in
//     /var/log/otel/spans.jsonl after a simulated request.
//
// This pins the operator-facing observability surface against the
// real wiring (wiring.go's r.Handle("/metrics", ...) plus
// run.go's TracingMiddleware), not against in-process recorders.
package realstack_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestRealStack_ProdBinary_MetricsEndpointExposesNamedCounters scrapes
// /metrics from the running binary and asserts each documented counter
// name appears in the exposition. The binary lazily registers
// counters at the first metric tick, so the test issues a couple of
// throwaway requests to /readyz first to ensure the API path counters
// have something to report (some metrics are zero-valued at boot but
// still appear in /metrics output via promhttp).
func TestRealStack_ProdBinary_MetricsEndpointExposesNamedCounters(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	stack := realstack.Boot(ctx, t, realstack.Options{})
	t.Cleanup(stack.Close)

	// Drive a few requests against the binary so the API counters are
	// non-trivial. /readyz lives on the same listener as /metrics.
	for i := 0; i < 3; i++ {
		resp, err := http.Get(stack.Binary.URL + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	resp, err := http.Get(stack.Binary.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// Prometheus exposition format must include the HELP/TYPE lines
	// for each counter the API metrics package declares. Pick a
	// handful that should appear unconditionally, regardless of
	// whether the counter has been incremented yet.
	want := []string{
		"fhir_subs_api_requests_total",
		"fhir_subs_api_request_duration_seconds",
		"fhir_subs_api_auth_failures_total",
		"fhir_subs_api_subscription_created_total",
		"fhir_subs_api_validation_failures_total",
	}
	got := string(body)
	for _, name := range want {
		if !strings.Contains(got, name) {
			t.Errorf("/metrics missing counter %q; first 4KB of body:\n%s", name, head(got, 4096))
		}
	}

	// Sanity: must look like Prometheus exposition.
	if !strings.Contains(got, "# HELP") {
		t.Errorf("/metrics body does not contain `# HELP` lines; first 1KB:\n%s", head(got, 1024))
	}
}

// TestRealStack_ProdBinary_OTelCollectorCapturesAtLeastOneSpan asserts
// the binary's OTel exporter is wired to the collector, requests
// against the binary produce spans, and at least one of those spans
// reaches the collector's file exporter
// (/var/log/otel/spans.jsonl in the otel-collector container).
//
// This is the round-trip evidence the tracing config landed and the
// pipeline is alive; without it, an operator could believe spans were
// flowing while a misconfigured exporter silently dropped them.
func TestRealStack_ProdBinary_OTelCollectorCapturesAtLeastOneSpan(t *testing.T) {
	requireDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), bootTimeout)
	defer cancel()

	// EnableTracing is the default; making it explicit so a future
	// flip in the default leaves this test still asserting what it
	// should.
	stack := realstack.Boot(ctx, t, realstack.Options{EnableTracing: true})
	t.Cleanup(stack.Close)

	// Drive several requests so the TracingMiddleware emits spans and
	// the collector's batch processor flushes them.
	for i := 0; i < 5; i++ {
		resp, err := http.Get(stack.Binary.URL + "/readyz")
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// Poll the spans file via `docker compose exec`. The batch
	// processor flushes every 100ms; give it generous slack for
	// flaky CI runners.
	deadline := time.Now().Add(15 * time.Second)
	var lastBody string
	for time.Now().Before(deadline) {
		body, err := readSpansFile(ctx, stack)
		if err == nil && hasNonEmptyJSONLine(body) {
			return // success
		}
		lastBody = body
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled while polling spans file: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("OTel collector spans.jsonl did not capture any span within 15s. Last body:\n%s",
		head(lastBody, 4096))
}

// readSpansFile shells out to `docker compose exec` against the
// otel-collector container in this test's compose project namespace
// and returns the contents of /var/log/otel/spans.jsonl.
func readSpansFile(ctx context.Context, stack *realstack.Stack) (string, error) {
	cmd := exec.CommandContext(ctx,
		"docker", "compose",
		"-p", stack.ProjectName(),
		"exec", "-T", "otel-collector",
		"sh", "-c", "cat /var/log/otel/spans.jsonl 2>/dev/null || true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker compose exec: %w: %s", err, string(out))
	}
	return string(out), nil
}

// hasNonEmptyJSONLine returns true when body contains at least one
// non-blank line. The file exporter writes one JSON object per
// captured ResourceSpans batch; we don't parse it (the schema is
// proto-shape JSON), we just assert content.
func hasNonEmptyJSONLine(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		// File-exporter lines look like {"resourceSpans":[...]} —
		// require at least an opening brace + a known key segment.
		if strings.HasPrefix(trim, "{") && strings.Contains(trim, "Spans") {
			return true
		}
	}
	return false
}

// head returns the first n bytes of s, with an ellipsis when truncated.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
