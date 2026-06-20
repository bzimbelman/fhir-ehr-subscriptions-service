// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestE2E_ProdBinary_LogFormatHonored asserts the production binary
// honors deployment.log_format. Pre-fix, run.go hardcoded
// `Format: "json"` so an operator who set `log_format: text` still
// got JSON, breaking the architecture.md local-dev runbook recipe.
//
// Story #160. Real binary, real config file, real stderr capture.
func TestE2E_ProdBinary_LogFormatHonored(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-logfmt",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://api.test.local",
		AuthAllowInsecureJWKS: true,
		LogFormat:             "text",
	})
	defer bin.Stop(t, 5*time.Second)

	// Wait briefly for the startup banner line to land.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hasTextFormatLine(bin.Stderr().Lines()) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range bin.Stderr().Lines() {
		t.Logf("captured: %s", l)
	}
	t.Fatalf("log_format: text was set but stderr contained no text-format slog line (every line was JSON)")
}

// hasTextFormatLine reports whether at least one captured line looks
// like slog's text handler output (e.g. `time=... level=INFO msg=...`)
// AND is not a JSON object. The slog text handler always emits
// `time=`, `level=`, `msg=` tokens space-separated; the JSON handler
// always wraps the whole record in `{...}`.
func hasTextFormatLine(lines []string) bool {
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "{") {
			continue
		}
		if strings.Contains(trimmed, "level=") && strings.Contains(trimmed, "msg=") {
			return true
		}
	}
	return false
}
