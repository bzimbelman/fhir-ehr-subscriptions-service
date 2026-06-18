// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"
)

// TestBuildPoolConfig_D3_ConnectTimeoutInjected asserts that the helper
// injects ConnConfig.ConnectTimeout when the URL did not specify one.
// Without this knob, pgxpool's internal connect-retry loop can outrun
// the caller's pingCtx and the time-to-first-error exceeds the operator's
// configured budget (D-3).
//
// D-3.
func TestBuildPoolConfig_D3_ConnectTimeoutInjected(t *testing.T) {
	t.Parallel()

	cfg, err := buildPoolConfig("postgres://u:p@localhost:1/db", 4*time.Second)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if got := cfg.ConnConfig.ConnectTimeout; got != 4*time.Second {
		t.Errorf("ConnectTimeout: got %v, want 4s", got)
	}
}

// TestBuildPoolConfig_D3_ConnectTimeoutZeroFallsBack asserts that a zero
// timeout falls back to a sensible default rather than the pgx default
// of "no per-attempt limit" — operators who don't tune the knob still
// get a fast-fail boot.
//
// D-3.
func TestBuildPoolConfig_D3_ConnectTimeoutZeroFallsBack(t *testing.T) {
	t.Parallel()

	cfg, err := buildPoolConfig("postgres://u:p@localhost:1/db", 0)
	if err != nil {
		t.Fatalf("buildPoolConfig: %v", err)
	}
	if cfg.ConnConfig.ConnectTimeout <= 0 {
		t.Errorf("ConnectTimeout: got %v, want positive default", cfg.ConnConfig.ConnectTimeout)
	}
	if cfg.ConnConfig.ConnectTimeout > 10*time.Second {
		t.Errorf("ConnectTimeout default %v unreasonably large; tighten or document", cfg.ConnConfig.ConnectTimeout)
	}
}

// TestBuildPoolConfig_D3_BadURLReturnsError asserts the helper surfaces
// a parse error rather than silently returning a zero config.
//
// D-3.
func TestBuildPoolConfig_D3_BadURLReturnsError(t *testing.T) {
	t.Parallel()

	_, err := buildPoolConfig("not-a-postgres-url://", time.Second)
	if err == nil {
		t.Fatalf("buildPoolConfig: want error for bad URL")
	}
}
