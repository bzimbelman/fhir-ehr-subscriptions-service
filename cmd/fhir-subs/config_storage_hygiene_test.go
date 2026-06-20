// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
	"time"
)

// Storage-hygiene batch RED tests covering OpenProject stories #216 and
// #217. The tests assert the operator-facing config surface a
// production deployment needs to tune the Postgres pool and the
// hardcoded ActivationTimeout / scheduler retry / processor poll knobs.
//
// Story #216 (OP #34): Surface Postgres pool tunables on cfg.Database.
// Today only Database.URL is modeled; pool tuning is invisible to the
// operator.
//
// Story #217 (OP #42, #43, #44): Surface auth.activation_timeout,
// pipeline.scheduler.retry.{initial,max,min,max_attempts}, and
// pipeline.processor.idle_poll_interval — all currently hardcoded in
// wiring.go.

// TestLoadConfig_StorageHygiene216_DatabasePoolTunables asserts that
// every pool tunable the operator needs lands on cfg.Database after a
// YAML decode. Defaults match the story acceptance criteria.
func TestLoadConfig_StorageHygiene216_DatabasePoolTunables(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
database:
  url: postgres://app:secret@db:5432/fhir
  max_conns: 50
  min_conns: 10
  max_conn_lifetime: 2h
  max_conn_idle_time: 15m
  health_check_period: 1m
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got, want := cfg.Database.MaxConns, int32(50); got != want {
		t.Errorf("Database.MaxConns: got %d want %d", got, want)
	}
	if got, want := cfg.Database.MinConns, int32(10); got != want {
		t.Errorf("Database.MinConns: got %d want %d", got, want)
	}
	if got, want := cfg.Database.MaxConnLifetime, 2*time.Hour; got != want {
		t.Errorf("Database.MaxConnLifetime: got %v want %v", got, want)
	}
	if got, want := cfg.Database.MaxConnIdleTime, 15*time.Minute; got != want {
		t.Errorf("Database.MaxConnIdleTime: got %v want %v", got, want)
	}
	if got, want := cfg.Database.HealthCheckPeriod, 1*time.Minute; got != want {
		t.Errorf("Database.HealthCheckPeriod: got %v want %v", got, want)
	}
}

// TestApplySets_StorageHygiene216_DatabasePoolTunables asserts that
// each pool tunable is reachable through the --set CLI override.
func TestApplySets_StorageHygiene216_DatabasePoolTunables(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key  string
		val  string
		got  func(*Config) any
		want any
	}{
		{
			key:  "database.max_conns",
			val:  "75",
			got:  func(c *Config) any { return c.Database.MaxConns },
			want: int32(75),
		},
		{
			key:  "database.min_conns",
			val:  "8",
			got:  func(c *Config) any { return c.Database.MinConns },
			want: int32(8),
		},
		{
			key:  "database.max_conn_lifetime",
			val:  "45m",
			got:  func(c *Config) any { return c.Database.MaxConnLifetime },
			want: 45 * time.Minute,
		},
		{
			key:  "database.max_conn_idle_time",
			val:  "10m",
			got:  func(c *Config) any { return c.Database.MaxConnIdleTime },
			want: 10 * time.Minute,
		},
		{
			key:  "database.health_check_period",
			val:  "20s",
			got:  func(c *Config) any { return c.Database.HealthCheckPeriod },
			want: 20 * time.Second,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := defaultConfig()
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("applySets %s: %v", c.key, err)
			}
			if got := c.got(cfg); got != c.want {
				t.Errorf("%s: got %v want %v", c.key, got, c.want)
			}
		})
	}
}

// TestLoadConfig_StorageHygiene217_ActivationAndPipelineTunables
// asserts the auth.activation_timeout,
// pipeline.scheduler.retry.{initial,max,min,max_attempts}, and
// pipeline.processor.idle_poll_interval keys land on the typed config.
func TestLoadConfig_StorageHygiene217_ActivationAndPipelineTunables(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
auth:
  activation_timeout: 45s
pipeline:
  processor:
    idle_poll_interval: 250ms
  scheduler:
    retry:
      initial: 2s
      max: 1m
      min: 750ms
      max_attempts: 12
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got, want := cfg.Auth.ActivationTimeout, 45*time.Second; got != want {
		t.Errorf("Auth.ActivationTimeout: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Processor.IdlePollInterval, 250*time.Millisecond; got != want {
		t.Errorf("Pipeline.Processor.IdlePollInterval: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.Initial, 2*time.Second; got != want {
		t.Errorf("Pipeline.Scheduler.Retry.Initial: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.Max, 1*time.Minute; got != want {
		t.Errorf("Pipeline.Scheduler.Retry.Max: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.Min, 750*time.Millisecond; got != want {
		t.Errorf("Pipeline.Scheduler.Retry.Min: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.MaxAttempts, int32(12); got != want {
		t.Errorf("Pipeline.Scheduler.Retry.MaxAttempts: got %d want %d", got, want)
	}
}

// TestApplySets_StorageHygiene217_ActivationAndPipelineTunables asserts
// every story #217 key is reachable through --set.
func TestApplySets_StorageHygiene217_ActivationAndPipelineTunables(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key  string
		val  string
		got  func(*Config) any
		want any
	}{
		{
			key:  "auth.activation_timeout",
			val:  "20s",
			got:  func(c *Config) any { return c.Auth.ActivationTimeout },
			want: 20 * time.Second,
		},
		{
			key:  "pipeline.processor.idle_poll_interval",
			val:  "350ms",
			got:  func(c *Config) any { return c.Pipeline.Processor.IdlePollInterval },
			want: 350 * time.Millisecond,
		},
		{
			key:  "pipeline.scheduler.retry.initial",
			val:  "3s",
			got:  func(c *Config) any { return c.Pipeline.Scheduler.Retry.Initial },
			want: 3 * time.Second,
		},
		{
			key:  "pipeline.scheduler.retry.max",
			val:  "90s",
			got:  func(c *Config) any { return c.Pipeline.Scheduler.Retry.Max },
			want: 90 * time.Second,
		},
		{
			key:  "pipeline.scheduler.retry.min",
			val:  "400ms",
			got:  func(c *Config) any { return c.Pipeline.Scheduler.Retry.Min },
			want: 400 * time.Millisecond,
		},
		{
			key:  "pipeline.scheduler.retry.max_attempts",
			val:  "20",
			got:  func(c *Config) any { return c.Pipeline.Scheduler.Retry.MaxAttempts },
			want: int32(20),
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := defaultConfig()
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("applySets %s: %v", c.key, err)
			}
			if got := c.got(cfg); got != c.want {
				t.Errorf("%s: got %v want %v", c.key, got, c.want)
			}
		})
	}
}

// TestDefaultConfig_StorageHygiene217_PipelineProcessorIdlePoll asserts
// defaultConfig pins the documented default of 200ms for the HL7
// processor poll cadence. Without this the wiring.go fallback at
// cmd/fhir-subs/wiring.go:667 stays as the source of truth, which
// hides the knob from the operator.
func TestDefaultConfig_StorageHygiene217_PipelineProcessorIdlePoll(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if got, want := cfg.Pipeline.Processor.IdlePollInterval, 200*time.Millisecond; got != want {
		t.Errorf("default Pipeline.Processor.IdlePollInterval: got %v want %v", got, want)
	}
}

// TestDefaultConfig_StorageHygiene217_AuthActivationTimeout asserts
// defaultConfig pins the documented 30s default for the activation
// timeout (currently hardcoded in wiring.go:554).
func TestDefaultConfig_StorageHygiene217_AuthActivationTimeout(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if got, want := cfg.Auth.ActivationTimeout, 30*time.Second; got != want {
		t.Errorf("default Auth.ActivationTimeout: got %v want %v", got, want)
	}
}

// TestDefaultConfig_StorageHygiene217_SchedulerRetry asserts
// defaultConfig pins the documented 1s/30s/500ms/8 defaults for the
// scheduler retry policy (currently hardcoded in wiring.go:782).
func TestDefaultConfig_StorageHygiene217_SchedulerRetry(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if got, want := cfg.Pipeline.Scheduler.Retry.Initial, 1*time.Second; got != want {
		t.Errorf("default Scheduler.Retry.Initial: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.Max, 30*time.Second; got != want {
		t.Errorf("default Scheduler.Retry.Max: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.Min, 500*time.Millisecond; got != want {
		t.Errorf("default Scheduler.Retry.Min: got %v want %v", got, want)
	}
	if got, want := cfg.Pipeline.Scheduler.Retry.MaxAttempts, int32(8); got != want {
		t.Errorf("default Scheduler.Retry.MaxAttempts: got %d want %d", got, want)
	}
}

// TestDefaultConfig_StorageHygiene216_DatabasePoolTunables asserts
// defaultConfig pins the documented MaxConns:25, MinConns:5,
// MaxConnLifetime:1h, MaxConnIdleTime:30m, HealthCheckPeriod:30s
// defaults from the story acceptance criteria.
func TestDefaultConfig_StorageHygiene216_DatabasePoolTunables(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if got, want := cfg.Database.MaxConns, int32(25); got != want {
		t.Errorf("default Database.MaxConns: got %d want %d", got, want)
	}
	if got, want := cfg.Database.MinConns, int32(5); got != want {
		t.Errorf("default Database.MinConns: got %d want %d", got, want)
	}
	if got, want := cfg.Database.MaxConnLifetime, 1*time.Hour; got != want {
		t.Errorf("default Database.MaxConnLifetime: got %v want %v", got, want)
	}
	if got, want := cfg.Database.MaxConnIdleTime, 30*time.Minute; got != want {
		t.Errorf("default Database.MaxConnIdleTime: got %v want %v", got, want)
	}
	if got, want := cfg.Database.HealthCheckPeriod, 30*time.Second; got != want {
		t.Errorf("default Database.HealthCheckPeriod: got %v want %v", got, want)
	}
}
