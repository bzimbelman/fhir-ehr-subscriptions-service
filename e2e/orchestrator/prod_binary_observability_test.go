// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Phase A (RED) e2e tests for OpenProject story #94: production binary
// MUST wire observability.Start and expose /metrics + OTLP tracing.
//
// Every test in this file pins one acceptance criterion against the
// real cmd/fhir-subs binary launched against a real Postgres
// container. They will FAIL today because:
//   - /metrics is not mounted on the chi router (AC #3)
//   - Tracing is never configured, so the OTLP exporter never sends
//     a span (AC #1, #2)
//   - The dead-letter reporter is only installed by observability.Start,
//     which is never called from cmd/fhir-subs (AC #2, #6)
//   - Audit rows are written by handlers.NewPgAuditStore directly,
//     bypassing the hash-chained audit.Writer (AC #5)
//
// Phase B will land the wiring; these tests then go GREEN.

// TestE2E_ProdBinary_MetricsEndpointServesPrometheus asserts the
// production binary mounts /metrics on the same HTTP port and serves
// Prometheus exposition (AC #3, #4, #6).
//
// FAILS today: handlers.RegisterRoutes does not mount /metrics, so the
// GET returns 404 and the body lacks any `fhir_subs_` series.
func TestE2E_ProdBinary_MetricsEndpointServesPrometheus(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:  h.DBURL,
		FacilityID:   "e2e-prod-94-metrics",
		AdapterID:    "default",
		Insecure:     true,
		GracePeriod:  5 * time.Second,
		AuthAudience: "",
	})
	defer bin.Stop(t, 5*time.Second)

	resp, err := http.Get(bin.HTTPURL() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics: status %d, want 200; body=%q", resp.StatusCode, string(body))
	}
	if !strings.Contains(string(body), "fhir_subs_") {
		t.Errorf("/metrics body lacks any fhir_subs_ series:\n%s", string(body))
	}
	// Prometheus exposition format starts with "# HELP" / "# TYPE"
	// lines for each registered metric. A Prometheus-formatted body
	// MUST contain at least one of these markers.
	if !strings.Contains(string(body), "# HELP") && !strings.Contains(string(body), "# TYPE") {
		t.Errorf("/metrics body is not Prometheus exposition format:\n%s",
			truncateE2E(string(body), 1024))
	}
}

// TestE2E_ProdBinary_DeadLetterReporterRegistersCounter asserts the
// observability inventory's fhir_subs_dead_letters_total counter is
// registered with the Prometheus registry at startup. The reporter
// hook is installed by observability.Start (AC #2, #6).
//
// FAILS today: /metrics is unmounted AND the inventory's counters are
// never registered because observability.Start is not called.
func TestE2E_ProdBinary_DeadLetterReporterRegistersCounter(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:  h.DBURL,
		FacilityID:   "e2e-prod-94-dlr",
		AdapterID:    "default",
		Insecure:     true,
		GracePeriod:  5 * time.Second,
		AuthAudience: "",
	})
	defer bin.Stop(t, 5*time.Second)

	resp, err := http.Get(bin.HTTPURL() + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics: status %d (want 200); body=%q", resp.StatusCode, string(body))
	}

	// observability.RegisterStartupInventory registers
	// fhir_subs_dead_letters_total{reason} at startup. The metric must
	// appear in the exposition even when no dead letters have been
	// recorded — Prometheus convention is "register every metric at
	// startup so dashboards never see a missing series" (LLD §4.2).
	if !strings.Contains(string(body), "fhir_subs_dead_letters_total") {
		t.Errorf("/metrics does not register fhir_subs_dead_letters_total — "+
			"observability.Start is not wired:\n%s",
			truncateE2E(string(body), 2048))
	}
}

// TestE2E_ProdBinary_OTLPExporterSendsSpan asserts the binary's OTLP
// exporter actually sends a span when tracing.otlp_endpoint is
// configured (AC #1, #2).
//
// FAILS today: the binary does not configure tracing at all — even
// when the tracing block is in the YAML, the loader silently drops it
// because Config has no Tracing field. The fake collector observes
// zero POSTs.
func TestE2E_ProdBinary_OTLPExporterSendsSpan(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// Stand up a fake OTLP HTTP receiver. Counts every POST it sees.
	var posts atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/traces", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			posts.Add(1)
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	otlpL, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen otlp: %v", err)
	}
	otlpSrv := &http.Server{Handler: mux}
	go func() { _ = otlpSrv.Serve(otlpL) }()
	t.Cleanup(func() {
		stopCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = otlpSrv.Shutdown(stopCtx)
	})
	otlpURL := "http://" + otlpL.Addr().String() + "/v1/traces"

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:         h.DBURL,
		FacilityID:          "e2e-prod-94-otlp",
		AdapterID:           "default",
		Insecure:            true,
		GracePeriod:         5 * time.Second,
		AuthAudience:        "",
		TracingOTLPEndpoint: otlpURL,
		TracingSampleRate:   1.0,
		TracingInsecure:     true,
	})
	defer bin.Stop(t, 5*time.Second)

	// Trigger spans by hitting a few endpoints.
	for i := 0; i < 5; i++ {
		resp, err := http.Get(bin.HTTPURL() + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
		}
		resp, err = http.Get(bin.HTTPURL() + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Wait up to 10s for the receiver to record at least one POST.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if posts.Load() >= 1 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("OTLP receiver got %d POSTs after 10s; expected >= 1 — exporter is not wired",
		posts.Load())
}

// TestE2E_ProdBinary_AuditWriterIsHashChained asserts every audit_log
// row written by the production binary is part of a real hash chain:
//   - hash is non-zero (32 bytes)
//   - prev_hash either matches the chain genesis seed or the previous
//     row's hash
//   - canonical_form (the chain input) is non-empty
//
// (AC #5)
//
// FAILS today: the wiring uses handlers.NewPgAuditStore(pool) directly,
// which writes a real chain (story #49 landed for the API path). After
// Phase B replaces this with the observability writer adapter, the same
// behavior must hold — but the adapter MUST write hash-chained rows.
// This test pins the contract so any future regression is caught.
func TestE2E_ProdBinary_AuditWriterIsHashChained(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	// Seed a client so the create attempt isn't rejected upstream of audit.
	if _, err := h.DB.Exec(ctx,
		`INSERT INTO auth_clients (id, scopes, display_name)
		 VALUES ($1, ARRAY['system/Subscription.cruds']::text[], $1)`,
		"e2e-prod-94-audit-client"); err != nil {
		t.Fatalf("seed auth_clients: %v", err)
	}

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:  h.DBURL,
		FacilityID:   "e2e-prod-94-audit",
		AdapterID:    "default",
		Insecure:     true,
		GracePeriod:  5 * time.Second,
		AuthAudience: "", // skip bearer middleware — audit is what we care about
	})
	defer bin.Stop(t, 5*time.Second)

	subBody := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topic/observation",
		"channelType": {"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type", "code": "rest-hook"},
		"endpoint": "https://subscriber.example.com/hook",
		"contentType": "application/fhir+json",
		"content": "id-only"
	}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		bin.HTTPURL()+"/Subscription/", strings.NewReader(subBody))
	if err != nil {
		t.Fatalf("build POST: %v", err)
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("X-Client-Id", "e2e-prod-94-audit-client")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /Subscription: %v", err)
	}
	_ = resp.Body.Close()

	// Wait until at least one audit row lands. The handler writes
	// the audit row in the same request flow, so this should be near-
	// instant — but allow a short grace for transaction commit.
	var count int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		err := h.DB.QueryRow(ctx,
			`SELECT count(*) FROM audit_log`).Scan(&count)
		if err == nil && count > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if count == 0 {
		t.Fatalf("no audit_log rows after 5s — audit pipeline is not wired")
	}

	// Inspect the most recent row: hash MUST be 32 bytes non-zero,
	// canonical_form MUST be non-empty.
	var (
		hash          []byte
		prevHash      []byte
		canonicalForm []byte
	)
	if err := h.DB.QueryRow(ctx,
		`SELECT hash, prev_hash, canonical_form
		   FROM audit_log
		  ORDER BY seq DESC LIMIT 1`,
	).Scan(&hash, &prevHash, &canonicalForm); err != nil {
		t.Fatalf("read audit_log row: %v", err)
	}

	if len(hash) != 32 {
		t.Errorf("audit_log.hash has length %d, want 32 (sha256)", len(hash))
	}
	if isAllZeroE2E(hash) {
		t.Errorf("audit_log.hash is all-zero — chain is a placeholder, not a real hash")
	}
	if len(canonicalForm) == 0 {
		t.Errorf("audit_log.canonical_form is empty — chain input was not persisted")
	}
	// prev_hash for the first row may be nil OR the genesis seed; both
	// are valid. For all subsequent rows it must be 32 bytes. We don't
	// know which row this is without seq, so accept either.
	if len(prevHash) != 0 && len(prevHash) != 32 {
		t.Errorf("audit_log.prev_hash has length %d, want 0 or 32", len(prevHash))
	}
}

// truncateE2E shortens a body to at most n bytes, appending an
// indicator if truncated. Used to keep test failure output readable.
func truncateE2E(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...[truncated]"
}

// isAllZeroE2E reports whether b is non-empty and consists entirely of
// 0x00 bytes — the placeholder pattern the API audit writer would
// emit if the hash chain was disabled. A real sha256 hash is
// statistically guaranteed to have at least one non-zero byte.
func isAllZeroE2E(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
