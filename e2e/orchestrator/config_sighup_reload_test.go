// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestE2E_ProdBinary_SIGHUPRereadsConfig (story #151) asserts that on
// SIGHUP the production binary re-reads the operator config file and
// applies hot-reloadable subsets. Pre-fix, the SIGHUP handler in
// wiring.go reloaded only the topic catalog — log_level, rate-limit
// knobs, and every other field were ignored.
//
// Mechanic: boot with log_level: info, rewrite the file in-place to
// log_level: debug, send SIGHUP, assert a "config reload applied"
// line lands.
//
// This test replaces the prior B-35 self-installed handler. It now
// exercises the full production wiring: real binary, real config
// file, real SIGHUP signal.
func TestE2E_ProdBinary_SIGHUPRereadsConfig(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	var cfgPath string
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-sighup-cfg",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://api.test.local",
		AuthAllowInsecureJWKS: true,
		LogLevel:              "info",
		ConfigPathSink:        &cfgPath,
	})
	defer bin.Stop(t, 5*time.Second)

	if cfgPath == "" {
		t.Fatalf("ConfigPathSink not populated")
	}
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read cfg: %v", err)
	}
	mutated := strings.Replace(string(body), "log_level: info", "log_level: debug", 1)
	if mutated == string(body) {
		t.Fatalf("config did not contain log_level: info")
	}
	if err := os.WriteFile(cfgPath, []byte(mutated), 0o600); err != nil {
		t.Fatalf("rewrite cfg: %v", err)
	}

	bin.SignalHUP(t)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bin.Stderr().ContainsLine("config reload applied") &&
			bin.Stderr().ContainsLine("sighup") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range bin.Stderr().Lines() {
		t.Logf("captured: %s", l)
	}
	t.Fatalf("SIGHUP did not produce a 'config reload applied' line with trigger=sighup — production wiring still ignores whole-config reloads")
}

// TestE2E_ProdBinary_SIGHUPWarnsOnImmutableField (story #151 AC) asserts
// that when the operator mutates a non-hot-reloadable field
// (database URL) and SIGHUPs the binary, the reload handler emits a
// WARN naming the rejected path and keeps the prior value live (i.e.
// no crash, no silent acceptance).
//
// AC: "Non-hot-reloadable fields (DB URL, MLLP listeners, TLS) MUST
// log a WARN if the user attempts to change them on reload."
func TestE2E_ProdBinary_SIGHUPWarnsOnImmutableField(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resetPipelineTables(t, ctx, h)

	var cfgPath string
	bin := startProdBinary(t, ctx, prodBinaryConfig{
		DatabaseURL:           h.DBURL,
		FacilityID:            "e2e-prod-sighup-immut",
		AdapterID:             "default",
		Insecure:              true,
		GracePeriod:           5 * time.Second,
		AuthAudience:          "https://api.test.local",
		AuthAllowInsecureJWKS: true,
		ConfigPathSink:        &cfgPath,
	})
	defer bin.Stop(t, 5*time.Second)

	body, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read cfg: %v", err)
	}
	mutated := strings.Replace(string(body),
		"url: "+h.DBURL,
		"url: postgres://operator-tried-to-rotate@localhost:5432/other",
		1)
	if mutated == string(body) {
		t.Fatalf("config did not contain expected database URL substring")
	}
	if err := os.WriteFile(cfgPath, []byte(mutated), 0o600); err != nil {
		t.Fatalf("rewrite cfg: %v", err)
	}

	bin.SignalHUP(t)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bin.Stderr().ContainsLine("config reload rejected") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, l := range bin.Stderr().Lines() {
		t.Logf("captured: %s", l)
	}
	t.Fatalf("SIGHUP after database URL change did not emit a 'config reload rejected' line — operator silently rotated an immutable field")
}
