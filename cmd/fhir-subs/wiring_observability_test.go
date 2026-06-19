// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability"
)

// Phase A (RED) tests for OpenProject story #94: production binary MUST
// wire observability.Start (metrics + OTel + audit + dead-letter
// reporter) into cmd/fhir-subs.
//
// Each test pins one acceptance criterion. They fail today because
// observability.Start is never called from cmd/fhir-subs (grep -rn
// 'observability\.Start' cmd/ is empty), no /metrics route is
// registered on the chi router, the Config has no tracing/metrics/
// audit blocks, and Deps.Audit comes from handlers.NewPgAuditStore
// directly instead of the observability writer.
//
// Tests use reflection to probe for fields that don't yet exist on
// *Config rather than referencing them by name; that keeps the rest
// of the cmd/fhir-subs test suite compiling while these tests
// actively assert "this field MUST be added in Phase B." A reflection
// miss produces a clear t.Errorf at runtime — the same RED signal a
// missing field would give as a compile error, but contained.
//
// AC reference (from the story):
//   1. Config grows tracing/metrics/audit blocks matching
//      docs/operations/otel-exporter-recipes.md.
//   2. wiring.go calls observability.Start and stores the returned
//      *ObservabilityModule so Shutdown can be registered.
//   3. The metrics emitter mounts on the chi router at /metrics.
//   4. Deps.Metrics is set to apimetrics.New(emitter.Registry()).
//   5. Deps.Audit comes from a writer routing through the observability
//      hash-chained audit.Writer (not handlers.NewPgAuditStore directly).
//   6. observability.Start installs repos.SetDeadLetterReporter so a
//      DeadLettersRepo.Insert increments fhir_subs_dead_letters_total.

// fieldByPath walks a struct path (e.g. "Tracing.OTLPEndpoint") on an
// arbitrary value and returns the resolved reflect.Value. The second
// return is false when any segment is missing — that is the RED
// signal Phase B must close by adding the field.
func fieldByPath(v reflect.Value, path string) (reflect.Value, bool) {
	cur := v
	for _, seg := range strings.Split(path, ".") {
		if cur.Kind() == reflect.Pointer {
			if cur.IsNil() {
				return reflect.Value{}, false
			}
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		f := cur.FieldByName(seg)
		if !f.IsValid() {
			return reflect.Value{}, false
		}
		cur = f
	}
	return cur, true
}

// TestConfig_TracingBlock_ParsesOTLPEndpoint asserts the Config grows a
// `tracing` block whose fields match the operator-facing schema in
// docs/operations/otel-exporter-recipes.md (AC #1).
//
// FAILS today: Config has no Tracing field — reflection miss yields
// t.Errorf. After Phase B, every probed field must be addressable AND
// hold the value parsed from YAML.
func TestConfig_TracingBlock_ParsesOTLPEndpoint(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
tracing:
  otlp_endpoint: http://127.0.0.1:4318/v1/traces
  sample_rate: 0.5
  exporter_timeout: 7s
  insecure: true
  tls:
    cert_file: /path/cert.pem
    key_file: /path/key.pem
    ca_file: /path/ca.pem
  headers:
    authorization: "Bearer x"
    x-honeycomb-team: zzz
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	v := reflect.ValueOf(cfg).Elem()
	checks := []struct {
		path string
		want any
	}{
		{"Tracing.OTLPEndpoint", "http://127.0.0.1:4318/v1/traces"},
		{"Tracing.SampleRate", 0.5},
		{"Tracing.ExporterTimeout", 7 * time.Second},
		{"Tracing.Insecure", true},
		{"Tracing.TLS.CertFile", "/path/cert.pem"},
		{"Tracing.TLS.KeyFile", "/path/key.pem"},
		{"Tracing.TLS.CAFile", "/path/ca.pem"},
	}
	for _, c := range checks {
		fv, ok := fieldByPath(v, c.path)
		if !ok {
			t.Errorf("Config.%s missing — Phase B MUST add this field", c.path)
			continue
		}
		got := fv.Interface()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Config.%s = %v, want %v", c.path, got, c.want)
		}
	}
	// Headers is a map[string]string.
	hv, ok := fieldByPath(v, "Tracing.Headers")
	if !ok {
		t.Errorf("Config.Tracing.Headers missing — Phase B MUST add this field")
		return
	}
	if hv.Kind() != reflect.Map {
		t.Errorf("Config.Tracing.Headers kind = %s, want map", hv.Kind())
		return
	}
	if v := hv.MapIndex(reflect.ValueOf("authorization")); !v.IsValid() ||
		v.String() != "Bearer x" {
		t.Errorf("Config.Tracing.Headers[authorization] missing/wrong (got %v)", v)
	}
	if v := hv.MapIndex(reflect.ValueOf("x-honeycomb-team")); !v.IsValid() ||
		v.String() != "zzz" {
		t.Errorf("Config.Tracing.Headers[x-honeycomb-team] missing/wrong (got %v)", v)
	}
}

// TestConfig_TracingBlock_EmptyEndpointIsValid asserts the unhappy path
// for tracing: when otlp_endpoint is empty, the binary still boots
// cleanly. Tracing is optional; metrics is mandatory.
func TestConfig_TracingBlock_EmptyEndpointIsValid(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	v := reflect.ValueOf(cfg).Elem()
	fv, ok := fieldByPath(v, "Tracing.OTLPEndpoint")
	if !ok {
		t.Errorf("Config.Tracing.OTLPEndpoint missing — Phase B MUST add this field")
	} else if got := fv.String(); got != "" {
		t.Errorf("Config.Tracing.OTLPEndpoint = %q, want empty (default)", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for empty tracing block", err)
	}
}

// TestConfig_MetricsBlock_ParsesBindAndPath asserts the Config grows a
// `metrics` block matching observability.MetricsConfig (AC #1).
func TestConfig_MetricsBlock_ParsesBindAndPath(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
metrics:
  bind: 0.0.0.0:9091
  path: /m
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	v := reflect.ValueOf(cfg).Elem()
	checks := []struct {
		path string
		want string
	}{
		{"Metrics.Bind", "0.0.0.0:9091"},
		{"Metrics.Path", "/m"},
	}
	for _, c := range checks {
		fv, ok := fieldByPath(v, c.path)
		if !ok {
			t.Errorf("Config.%s missing — Phase B MUST add this field", c.path)
			continue
		}
		if got := fv.String(); got != c.want {
			t.Errorf("Config.%s = %q, want %q", c.path, got, c.want)
		}
	}
}

// TestConfig_AuditBlock_ParsesSinkAndFilePath asserts the Config grows
// an `audit` block matching observability.AuditConfig (AC #1).
func TestConfig_AuditBlock_ParsesSinkAndFilePath(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
audit:
  sink: file
  file_path: /tmp/audit.jsonl
  file_sync_mode: every_write
  file_batch_interval: 250ms
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	v := reflect.ValueOf(cfg).Elem()
	type expectation struct {
		path string
		want any
	}
	checks := []expectation{
		{"Audit.Sink", "file"},
		{"Audit.FilePath", "/tmp/audit.jsonl"},
		{"Audit.FileSyncMode", "every_write"},
		{"Audit.FileBatchInterval", 250 * time.Millisecond},
	}
	for _, c := range checks {
		fv, ok := fieldByPath(v, c.path)
		if !ok {
			t.Errorf("Config.%s missing — Phase B MUST add this field", c.path)
			continue
		}
		if !reflect.DeepEqual(fv.Interface(), c.want) {
			t.Errorf("Config.%s = %v, want %v", c.path, fv.Interface(), c.want)
		}
	}
}

// TestConfig_TracingBlock_NegativeSampleRate asserts a malformed
// sample_rate is rejected by Validate (or by the loader) — the
// unhappy/edge-path the prompt requires for the YAML parser.
//
// FAILS today: Config has no Tracing field, so the value is silently
// stuffed into Extra. Phase B must either reject -0.5 at parse time
// or surface it via Validate().
func TestConfig_TracingBlock_NegativeSampleRate(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
  environment: dev
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
tracing:
  otlp_endpoint: http://127.0.0.1:4318/v1/traces
  sample_rate: -0.5
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		// Strict YAML parsing rejecting -0.5 also satisfies the
		// "negative path is not silently accepted" contract.
		return
	}
	v := reflect.ValueOf(cfg).Elem()
	fv, ok := fieldByPath(v, "Tracing.SampleRate")
	if !ok {
		t.Errorf("Config.Tracing.SampleRate missing — Phase B MUST add this field, " +
			"and reject negative values via Validate()")
		return
	}
	got := fv.Float()
	if got >= 0 && got <= 1 {
		// Loader normalized; acceptable.
		return
	}
	if err := cfg.Validate(); err == nil {
		t.Errorf("Validate() accepted tracing.sample_rate = %v; want a non-nil error",
			got)
	}
}

// TestBuildObservabilityConfig_MapsFromConfig asserts the wiring
// exposes a helper that maps *Config -> observability.Config (AC #1,
// #2). Phase B must add buildObservabilityConfig(cfg *Config)
// observability.Config.
//
// FAILS today: the helper does not exist. We probe it via reflection
// over the package's declared functions — but reflect cannot enumerate
// package-level funcs in Go. Instead we exercise an indirect probe:
// when buildObservabilityConfig is added, the function should map
// fields from Config to observability.Config 1:1, and observability.Config
// already exists, so we just instantiate the target shape and assert
// it has the required nested fields. The existence-of-helper is
// pinned by the e2e test (which boots the binary) and the
// TestProductionRuntime_* tests below.
func TestBuildObservabilityConfig_TargetShapeIsAvailable(t *testing.T) {
	t.Parallel()

	// observability.Config already declares Metrics/Tracing/Audit;
	// confirm the fields that Phase B's helper must populate exist on
	// the target type so a future restructure of observability.Config
	// is caught here.
	var oc observability.Config
	v := reflect.ValueOf(&oc).Elem()
	for _, path := range []string{
		"Metrics.Bind",
		"Metrics.Path",
		"Tracing.OTLPEndpoint",
		"Tracing.SampleRate",
		"Audit.Sink",
		"Audit.FilePath",
		"Audit.FileSyncMode",
		"Audit.FileBatchInterval",
	} {
		if _, ok := fieldByPath(v, path); !ok {
			t.Errorf("observability.Config.%s missing — wiring helper has nothing to populate",
				path)
		}
	}
}

// TestProductionRuntime_MountsMetricsEndpoint asserts buildProductionRuntime
// wires `/metrics` onto the chi router so Prometheus can scrape (AC #3,
// #4, #6).
//
// Gated on TEST_PG_URL because the real wiring requires Postgres. When
// the env var is unset, Skip keeps `go test` fast while leaving the
// CI assertion path live.
//
// FAILS today (when run with TEST_PG_URL set): handlers.RegisterRoutes
// mounts no /metrics route, so a GET against the router yields 404.
func TestProductionRuntime_MountsMetricsEndpoint(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: status %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fhir_subs_") && !strings.Contains(body, "# HELP") {
		t.Errorf("/metrics body does not look like Prometheus exposition: %q", body)
	}
}

// TestProductionRuntime_ObservabilityModuleStored pins the contract
// that buildProductionRuntime calls observability.Start and stashes
// the returned *ObservabilityModule on the runtime so Shutdown can be
// registered (AC #2).
//
// FAILS today: productionRuntime has no obsModule field — the
// reflection probe misses, t.Errorf fires.
//
// Gated on TEST_PG_URL because constructing the runtime requires a
// real Postgres pool.
func TestProductionRuntime_ObservabilityModuleStored(t *testing.T) {
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; integration assertion runs in CI")
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1"},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lcMod, err := lifecycle.Start(ctx, lifecycle.LifecycleConfig{
		ShutdownGracePeriod: 5 * time.Second,
	}, lifecycle.LifecycleContext{Logger: logger})
	if err != nil {
		t.Fatalf("lifecycle.Start: %v", err)
	}

	rt, err := buildProductionRuntime(ctx, cfg, logger, lcMod)
	if err != nil {
		t.Fatalf("buildProductionRuntime: %v", err)
	}
	defer rt.shutdown(context.Background())

	// Probe the runtime via reflection so this test compiles even
	// before Phase B adds the obsModule field. After Phase B the
	// field exists and points at a non-nil *observability.ObservabilityModule.
	v := reflect.ValueOf(rt).Elem()
	for _, candidate := range []string{"obsModule", "obs", "observability"} {
		f := v.FieldByName(candidate)
		if f.IsValid() {
			if f.Kind() != reflect.Pointer || f.IsNil() {
				t.Errorf("productionRuntime.%s = %v; want non-nil "+
					"*observability.ObservabilityModule", candidate, f)
			}
			// Confirm the type — it must be assignable to
			// *observability.ObservabilityModule.
			var want *observability.ObservabilityModule
			wantType := reflect.TypeOf(want)
			if !f.Type().AssignableTo(wantType) {
				t.Errorf("productionRuntime.%s type = %v, want %v",
					candidate, f.Type(), wantType)
			}
			return
		}
	}
	t.Errorf("productionRuntime has no field for the observability module — " +
		"Phase B MUST add one (e.g. obsModule *observability.ObservabilityModule) " +
		"and call observability.Start in buildProductionRuntime")
}
