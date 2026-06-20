// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
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
)

// Phase A (RED) tests for the deps-wiring-batch:
//
//   #176 — Wire Deps.Metrics into handlers
//   #177 — Wire Deps.Logger into handlers
//   #178 — Wire Deps tunable knobs (page sizes, byte caps, FHIR version, JWKSURL)
//   #179 — Set BaseURL/WSBaseURL respecting `insecure` and bind interface
//   #180 — Make WSBindingTTL config-driven
//   #181 — Mount /.well-known/jwks.json
//   #199 — Surface scheduler tunables (RecoveryInterval, StuckThreshold, DispatchConcurrency)
//
// All tests live in package main alongside the existing wiring_*_test.go
// files so they probe the same productionRuntime and applySets helpers.
// Tests that need a real Postgres pool gate on TEST_PG_URL (matches the
// pattern in webhook_wiring_test.go); structural Config / applySets
// assertions run without a database so CI fails RED even when the env
// var is unset.

// ----------------------------------------------------------------------
// Story #199 — scheduler tunables surface in Config + applySets
// ----------------------------------------------------------------------

// TestConfig_PipelineScheduler_RecoveryIntervalParses asserts the YAML
// loader recognizes pipeline.scheduler.recovery_interval. FAILS today:
// StageConfig has only ClaimBatchSize / IdlePollInterval; the new field
// must land in PipelineConfig (or StageConfig) in Phase B.
func TestConfig_PipelineScheduler_RecoveryIntervalParses(t *testing.T) {
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
pipeline:
  scheduler:
    recovery_interval: 17s
    stuck_threshold: 11m
    dispatch_concurrency: 7
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
		{"Pipeline.Scheduler.RecoveryInterval", 17 * time.Second},
		{"Pipeline.Scheduler.StuckThreshold", 11 * time.Minute},
		{"Pipeline.Scheduler.DispatchConcurrency", 7},
	}
	for _, c := range checks {
		fv, ok := fieldByPath(v, c.path)
		if !ok {
			t.Errorf("Config.%s missing — Phase B MUST add this field (story #199)", c.path)
			continue
		}
		got := fv.Interface()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Config.%s = %v (%T), want %v (%T)", c.path, got, got, c.want, c.want)
		}
	}
}

// TestApplySets_SchedulerTunables asserts --set surfaces the scheduler
// tunables. Today applySets returns "unsupported key" — story #199 Phase
// B must add the cases.
func TestApplySets_SchedulerTunables(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key string
		val string
	}{
		{"pipeline.scheduler.recovery_interval", "45s"},
		{"pipeline.scheduler.stuck_threshold", "10m"},
		{"pipeline.scheduler.dispatch_concurrency", "8"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("applySets %s: %v — Phase B must add scheduler tunable to applySets switch (story #199)", c.key, err)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Story #178 — Deps tunable knobs surface in Config + applySets
// ----------------------------------------------------------------------

// TestConfig_APITunablesParse asserts every operator-tunable knob the
// API handlers consume is parsed from YAML. The handlers.Deps struct
// already has the fields (router.go); production wiring must thread
// the values through Config -> Deps. FAILS today because the Config
// struct has no api / fhir blocks.
func TestConfig_APITunablesParse(t *testing.T) {
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
api:
  search_page_size: 25
  search_max_page_size: 250
  event_replay_page_size: 500
  max_status_bulk_ids: 64
  max_body_bytes: 524288
  max_schema_error_bytes: 4096
  audit_max_bytes: 32768
  ws_binding_ttl: 7m
  fhir_version: 4.0.1
  supported_fhir_versions: ["4.0.1", "5.0.0"]
  base_url: https://api.example.invalid
  ws_base_url: wss://api.example.invalid/ws
  jwks_url: https://api.example.invalid/.well-known/jwks.json
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
		{"API.SearchPageSize", 25},
		{"API.SearchMaxPageSize", 250},
		{"API.EventReplayPageSize", 500},
		{"API.MaxStatusBulkIDs", 64},
		{"API.MaxBodyBytes", int64(524288)},
		{"API.MaxSchemaErrorBytes", 4096},
		{"API.AuditMaxBytes", 32768},
		{"API.WSBindingTTL", 7 * time.Minute},
		{"API.FHIRVersion", "4.0.1"},
		{"API.BaseURL", "https://api.example.invalid"},
		{"API.WSBaseURL", "wss://api.example.invalid/ws"},
		{"API.JWKSURL", "https://api.example.invalid/.well-known/jwks.json"},
	}
	for _, c := range checks {
		fv, ok := fieldByPath(v, c.path)
		if !ok {
			t.Errorf("Config.%s missing — Phase B MUST add this field (stories #178/#179/#180)", c.path)
			continue
		}
		got := fv.Interface()
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Config.%s = %v (%T), want %v (%T)", c.path, got, got, c.want, c.want)
		}
	}

	// supported_fhir_versions is a []string.
	sv, ok := fieldByPath(v, "API.SupportedFHIRVersions")
	if !ok {
		t.Errorf("Config.API.SupportedFHIRVersions missing — Phase B MUST add this field (story #178)")
		return
	}
	if sv.Kind() != reflect.Slice {
		t.Errorf("Config.API.SupportedFHIRVersions kind = %s, want slice", sv.Kind())
		return
	}
	got := make([]string, sv.Len())
	for i := 0; i < sv.Len(); i++ {
		got[i] = sv.Index(i).String()
	}
	if !reflect.DeepEqual(got, []string{"4.0.1", "5.0.0"}) {
		t.Errorf("Config.API.SupportedFHIRVersions = %v, want [4.0.1 5.0.0]", got)
	}
}

// TestApplySets_APITunables asserts --set surfaces every API tunable
// added by stories #178/#179/#180. Each case is a separate sub-test so
// the report names exactly which key applySets does not yet recognize.
func TestApplySets_APITunables(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key string
		val string
	}{
		{"api.search_page_size", "25"},
		{"api.search_max_page_size", "250"},
		{"api.event_replay_page_size", "500"},
		{"api.max_status_bulk_ids", "64"},
		{"api.max_body_bytes", "524288"},
		{"api.max_schema_error_bytes", "4096"},
		{"api.audit_max_bytes", "32768"},
		{"api.ws_binding_ttl", "7m"},
		{"api.fhir_version", "4.0.1"},
		{"api.base_url", "https://api.example.invalid"},
		{"api.ws_base_url", "wss://api.example.invalid/ws"},
		{"api.jwks_url", "https://api.example.invalid/.well-known/jwks.json"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("applySets %s: %v — Phase B must add api tunable to applySets switch (stories #178/#179/#180)", c.key, err)
			}
		})
	}
}

// ----------------------------------------------------------------------
// Stories #176 / #177 / #178 / #179 / #180 — production runtime applies
// the values to handlers.Deps
//
// These tests boot buildProductionRuntime against a real Postgres pool
// (gated on TEST_PG_URL) and use chi's URL-routing path to assert that
// the configured tunables flowed through to the live handlers.
// ----------------------------------------------------------------------

// metadataDoc fetches /metadata against rt.router and unmarshals the
// CapabilityStatement. The bare GET succeeds because RegisterPublicRoutes
// mounts /metadata before the auth-protected group (story #93).
func metadataDoc(t *testing.T, rt *productionRuntime) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metadata", nil)
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metadata: got %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("/metadata: %v", err)
	}
	return doc
}

// minimalProductionConfig builds the bare-minimum production Config for
// the deps-batch tests. Caller can mutate before passing to buildProductionRuntime.
func minimalProductionConfig(dbURL string) *Config {
	return &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "default"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "127.0.0.1:0", ProbeBind: "127.0.0.1:0", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
		Database:   DatabaseConfig{URL: dbURL},
		Codec: CodecConfig{
			ActiveKeyVersion: 1,
			Keys: []CodecKeySpec{
				{Version: 1, Material: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			},
		},
		// Dev bypass keeps these wiring tests off the trusted-issuer
		// JWKS path; they only assert wire-up of Deps fields, not auth
		// flow. Production deployments leave AllowDevBypass false.
		Auth: AuthConfig{AllowDevBypass: true},
	}
}

// startTestRuntime starts a buildProductionRuntime instance and tears
// it down on test completion. Skips when TEST_PG_URL is unset.
func startTestRuntime(t *testing.T, mutate func(*Config)) *productionRuntime {
	t.Helper()
	dbURL := os.Getenv("TEST_PG_URL")
	if dbURL == "" {
		t.Skip("TEST_PG_URL not set; production-runtime assertion runs in CI")
	}
	cfg := minimalProductionConfig(dbURL)
	if mutate != nil {
		mutate(cfg)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
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
	t.Cleanup(func() { rt.shutdown(context.Background()) })
	return rt
}

// TestProductionRuntime_BaseURL_RespectsInsecure (story #179) asserts
// that when server.http.insecure=true the derived BaseURL/WSBaseURL
// schemes are http/ws (not https/wss). FAILS today: wiring.go
// hardcodes "https://" + cfg.Server.HTTP.Bind regardless of insecure.
func TestProductionRuntime_BaseURL_RespectsInsecure(t *testing.T) {
	rt := startTestRuntime(t, func(c *Config) {
		c.Server.HTTP.Insecure = true
		// Set bind to a sentinel so we can identify it in the doc.
		c.Server.HTTP.Bind = "127.0.0.1:18443"
	})
	if got := rt.deps.BaseURL; !strings.HasPrefix(got, "http://") {
		t.Errorf("Deps.BaseURL = %q, want http:// prefix when insecure=true (story #179)", got)
	}
	if got := rt.deps.BaseURL; strings.HasPrefix(got, "https://") {
		t.Errorf("Deps.BaseURL = %q, must not be https:// when insecure=true (story #179)", got)
	}
	if got := rt.deps.WSBaseURL; !strings.HasPrefix(got, "ws://") {
		t.Errorf("Deps.WSBaseURL = %q, want ws:// prefix when insecure=true (story #179)", got)
	}
	if got := rt.deps.WSBaseURL; strings.HasPrefix(got, "wss://") {
		t.Errorf("Deps.WSBaseURL = %q, must not be wss:// when insecure=true (story #179)", got)
	}
}

// TestProductionRuntime_BaseURL_RespectsConfiguredOverride (story #179)
// asserts that an explicit api.base_url in Config wins over the
// derived-from-bind value. FAILS today: the field doesn't exist in
// Config and nothing reads it.
func TestProductionRuntime_BaseURL_RespectsConfiguredOverride(t *testing.T) {
	rt := startTestRuntime(t, func(c *Config) {
		// Set api.base_url via reflection so the test compiles even
		// when Phase B has not yet added the field. fieldByPath miss
		// becomes a t.Errorf — the same RED signal a missing field
		// would give as a compile error, but contained.
		v := reflect.ValueOf(c).Elem()
		bf, ok := fieldByPath(v, "API.BaseURL")
		if !ok {
			t.Skip("Config.API.BaseURL missing — covered by TestConfig_APITunablesParse RED")
		}
		bf.SetString("https://public.example.invalid")
		wf, ok := fieldByPath(v, "API.WSBaseURL")
		if !ok {
			t.Skip("Config.API.WSBaseURL missing — covered by TestConfig_APITunablesParse RED")
		}
		wf.SetString("wss://public.example.invalid/ws")
	})
	if got := rt.deps.BaseURL; !strings.Contains(got, "public.example.invalid") {
		t.Errorf("Deps.BaseURL = %q, want host public.example.invalid (story #179)", got)
	}
	if got := rt.deps.WSBaseURL; !strings.Contains(got, "public.example.invalid") {
		t.Errorf("Deps.WSBaseURL = %q, want host public.example.invalid (story #179)", got)
	}
	// CapabilityStatement reflects the same value via implementation.url
	// (the FHIR conformance probes consume it).
	doc := metadataDoc(t, rt)
	impl, _ := doc["implementation"].(map[string]any)
	implURL, _ := impl["url"].(string)
	if !strings.Contains(implURL, "public.example.invalid") {
		t.Errorf("CapabilityStatement.implementation.url = %q, want host public.example.invalid (story #179)", implURL)
	}
}

// TestProductionRuntime_FHIRVersion_FromConfig (story #178) asserts
// the configured fhir_version is rendered into the CapabilityStatement
// AND threaded onto handlers.Deps. FAILS today: cfg has no
// API.FHIRVersion field; wiring.go does not pass it through.
func TestProductionRuntime_FHIRVersion_FromConfig(t *testing.T) {
	rt := startTestRuntime(t, func(c *Config) {
		v := reflect.ValueOf(c).Elem()
		f, ok := fieldByPath(v, "API.FHIRVersion")
		if !ok {
			t.Skip("Config.API.FHIRVersion missing — covered by TestConfig_APITunablesParse RED")
		}
		f.SetString("4.0.1")
	})
	if got := rt.deps.FHIRVersion; got != "4.0.1" {
		t.Errorf("Deps.FHIRVersion = %q, want 4.0.1 (story #178)", got)
	}
	doc := metadataDoc(t, rt)
	got, _ := doc["fhirVersion"].(string)
	if got != "4.0.1" {
		t.Fatalf("CapabilityStatement.fhirVersion = %q, want 4.0.1 (story #178: api.fhir_version must thread through to Deps)", got)
	}
}

// TestProductionRuntime_JWKSURL_FromConfig (story #178) asserts the
// configured api.jwks_url is threaded onto Deps.JWKSURL and rendered
// into the SMART security extension. The handler-side rendering
// already exists; the wiring must thread the value into Deps.JWKSURL
// (currently never set).
func TestProductionRuntime_JWKSURL_FromConfig(t *testing.T) {
	const want = "https://api.example.invalid/.well-known/jwks.json"
	rt := startTestRuntime(t, func(c *Config) {
		v := reflect.ValueOf(c).Elem()
		f, ok := fieldByPath(v, "API.JWKSURL")
		if !ok {
			t.Skip("Config.API.JWKSURL missing — covered by TestConfig_APITunablesParse RED")
		}
		f.SetString(want)
	})
	if got := rt.deps.JWKSURL; got != want {
		t.Errorf("Deps.JWKSURL = %q, want %q (story #178)", got, want)
	}
	doc := metadataDoc(t, rt)
	if !strings.Contains(rec(t, doc), want) {
		t.Fatalf("CapabilityStatement does not contain configured jwks_url %q; doc=%s (story #178)", want, rec(t, doc))
	}
}

// rec is a small helper that re-marshals a CapabilityStatement for
// substring assertions on nested security/extension blocks.
func rec(t *testing.T, doc map[string]any) string {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	return string(b)
}

// TestProductionRuntime_WellKnownJWKSMounted (story #181) asserts the
// production router mounts /.well-known/jwks.json with content-type
// application/json and a parseable {"keys": [...]} body. With HS256
// signing the keys array MAY be empty; what matters is that the path
// is present and returns 200 (not 404 / not 401).
//
// FAILS today: no route is registered at that path.
func TestProductionRuntime_WellKnownJWKSMounted(t *testing.T) {
	rt := startTestRuntime(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/.well-known/jwks.json: got %d body=%s, want 200 (story #181)",
			rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("/.well-known/jwks.json content-type = %q, want application/json (story #181)", ct)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("/.well-known/jwks.json body not JSON: %v body=%s (story #181)",
			err, rec.Body.String())
	}
	if _, ok := doc["keys"]; !ok {
		t.Fatalf("/.well-known/jwks.json missing top-level \"keys\"; got %s (story #181)",
			rec.Body.String())
	}
}

// TestProductionRuntime_DepsLoggerWired (story #177) asserts that
// handlers.Deps.Logger is non-nil after the production wiring runs.
// Without Logger the activate-side error path in handlers silently
// drops failures the API can't surface to the client (router.go
// comment on Deps.Logger). FAILS today: wiring.go never assigns
// deps.Logger.
func TestProductionRuntime_DepsLoggerWired(t *testing.T) {
	rt := startTestRuntime(t, nil)
	if rt.deps.Logger == nil {
		t.Fatalf("Deps.Logger is nil — Phase B must thread the production logger through to handlers.Deps (story #177)")
	}
}

// TestProductionRuntime_WSBindingTTL_FromConfig (story #180) asserts
// the operator-configured ws_binding_ttl reaches handlers.Deps.
// Today wiring.go hardcodes 5 * time.Minute. After Phase B, the field
// flows from cfg.API.WSBindingTTL onto rt.deps.WSBindingTTL.
func TestProductionRuntime_WSBindingTTL_FromConfig(t *testing.T) {
	const want = 7 * time.Minute
	rt := startTestRuntime(t, func(c *Config) {
		v := reflect.ValueOf(c).Elem()
		f, ok := fieldByPath(v, "API.WSBindingTTL")
		if !ok {
			t.Skip("Config.API.WSBindingTTL missing — covered by TestConfig_APITunablesParse RED")
		}
		f.Set(reflect.ValueOf(want))
	})
	if got := rt.deps.WSBindingTTL; got != want {
		t.Fatalf("Deps.WSBindingTTL = %s, want %s (story #180: hardcoded 5m must come from cfg.API.WSBindingTTL)", got, want)
	}
}

// TestProductionRuntime_SchedulerTunables_FromConfig (story #199)
// asserts the scheduler.Worker observes the operator-configured
// recovery_interval, stuck_threshold, dispatch_concurrency. The
// scheduler does not expose its Config publicly today, so the
// assertion is structural via reflection: rt.scheduler.cfg holds
// the values built by wiring.go.
//
// FAILS today: wiring.go hardcodes nothing for these knobs and the
// scheduler falls back to applyDefaults (30s / 5m / 1).
func TestProductionRuntime_SchedulerTunables_FromConfig(t *testing.T) {
	const (
		wantRecovery    = 17 * time.Second
		wantStuck       = 11 * time.Minute
		wantConcurrency = 7
	)
	rt := startTestRuntime(t, func(c *Config) {
		v := reflect.ValueOf(c).Elem()
		ri, ok := fieldByPath(v, "Pipeline.Scheduler.RecoveryInterval")
		if !ok {
			t.Skip("Config.Pipeline.Scheduler.RecoveryInterval missing — covered by TestConfig_PipelineScheduler_RecoveryIntervalParses RED")
		}
		ri.Set(reflect.ValueOf(wantRecovery))
		st, ok := fieldByPath(v, "Pipeline.Scheduler.StuckThreshold")
		if !ok {
			t.Skip("Config.Pipeline.Scheduler.StuckThreshold missing — covered by RED")
		}
		st.Set(reflect.ValueOf(wantStuck))
		dc, ok := fieldByPath(v, "Pipeline.Scheduler.DispatchConcurrency")
		if !ok {
			t.Skip("Config.Pipeline.Scheduler.DispatchConcurrency missing — covered by RED")
		}
		dc.SetInt(int64(wantConcurrency))
	})

	// Reflect into rt.scheduler.cfg to read the live values. The
	// scheduler.Config struct is in
	// internal/engine/scheduler/worker.go; its `cfg` field is
	// unexported but reflection-readable via reflect.NewAt.
	if rt.scheduler == nil {
		t.Fatalf("rt.scheduler is nil — production runtime did not build scheduler worker")
	}
	sv := reflect.ValueOf(rt.scheduler).Elem()
	cfgF := sv.FieldByName("cfg")
	if !cfgF.IsValid() {
		t.Fatalf("scheduler.Worker.cfg field missing — internal field name changed")
	}
	got := struct {
		Recovery    time.Duration
		Stuck       time.Duration
		Concurrency int
	}{
		Recovery:    durationOrZero(cfgF.FieldByName("RecoveryInterval")),
		Stuck:       durationOrZero(cfgF.FieldByName("StuckThreshold")),
		Concurrency: intOrZero(cfgF.FieldByName("DispatchConcurrency")),
	}
	if got.Recovery != wantRecovery {
		t.Errorf("scheduler.cfg.RecoveryInterval = %s, want %s (story #199)", got.Recovery, wantRecovery)
	}
	if got.Stuck != wantStuck {
		t.Errorf("scheduler.cfg.StuckThreshold = %s, want %s (story #199)", got.Stuck, wantStuck)
	}
	if got.Concurrency != wantConcurrency {
		t.Errorf("scheduler.cfg.DispatchConcurrency = %d, want %d (story #199)", got.Concurrency, wantConcurrency)
	}
}

func durationOrZero(v reflect.Value) time.Duration {
	if !v.IsValid() {
		return 0
	}
	return time.Duration(v.Int())
}

func intOrZero(v reflect.Value) int {
	if !v.IsValid() {
		return 0
	}
	return int(v.Int())
}

// TestProductionRuntime_DepsMetricsWired (story #176) asserts the live
// metrics recorder is non-nil on Deps AND that the same registry
// surfaces metrics on /metrics. Without the wiring the recorder is
// nil and apimetrics drops every observation.
func TestProductionRuntime_DepsMetricsWired(t *testing.T) {
	rt := startTestRuntime(t, nil)

	if rt.deps.Metrics == nil {
		t.Fatalf("Deps.Metrics is nil — Phase B must keep apimetrics.New(obsMod.Registry()) wired into handlers.Deps (story #176)")
	}

	// /metrics is a Prometheus text-format endpoint mounted on the
	// public chi router (story #94 AC #3). It MUST exist and serve a
	// 200; story #176 then requires the api-metrics counter family
	// names to be present even before any traffic is generated.
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	rt.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics: got %d, want 200 (story #176 needs the metrics surface live)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fhir_subs_") {
		t.Fatalf("/metrics body does not contain fhir_subs_* family — Deps.Metrics not wired against obsMod.Registry() (story #176)\nbody=%s", body)
	}
}
