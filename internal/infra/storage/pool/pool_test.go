// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package pool_test

import (
	"context"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/pool"
)

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := pool.Config{URL: "postgres://localhost/x"}
	cfg.ApplyDefaults()
	if cfg.MinConnections == 0 {
		t.Error("MinConnections should default to non-zero")
	}
	if cfg.MaxConnections == 0 {
		t.Error("MaxConnections should default to non-zero")
	}
	if cfg.StatementTimeout == 0 {
		t.Error("StatementTimeout should default to non-zero")
	}
	if cfg.AcquireTimeout == 0 {
		t.Error("AcquireTimeout should default to non-zero")
	}
	if cfg.MaxConnectionLifetime == 0 {
		t.Error("MaxConnectionLifetime should default to non-zero")
	}
	if cfg.HealthCheckInterval == 0 {
		t.Error("HealthCheckInterval should default to non-zero")
	}
	if cfg.IdleTimeout == 0 {
		t.Error("IdleTimeout should default to non-zero")
	}
	if cfg.ApplicationName == "" {
		t.Error("ApplicationName should default to non-empty")
	}
}

func TestConfigDefaultsAreNotOverwritten(t *testing.T) {
	t.Parallel()

	cfg := pool.Config{
		URL:              "postgres://localhost/x",
		MaxConnections:   42,
		StatementTimeout: 7 * time.Second,
		ApplicationName:  "custom",
	}
	cfg.ApplyDefaults()
	if cfg.MaxConnections != 42 {
		t.Errorf("MaxConnections changed: got %d", cfg.MaxConnections)
	}
	if cfg.StatementTimeout != 7*time.Second {
		t.Errorf("StatementTimeout changed: got %v", cfg.StatementTimeout)
	}
	if cfg.ApplicationName != "custom" {
		t.Errorf("ApplicationName changed: got %q", cfg.ApplicationName)
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	cfg := pool.Config{}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when URL is empty")
	}

	cfg.URL = "postgres://localhost/x"
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	cfg.MinConnections = 10
	cfg.MaxConnections = 5
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when min > max")
	}
}

func TestOpenWithBadURL(t *testing.T) {
	t.Parallel()

	cfg := pool.Config{URL: "this-is-not-a-postgres-url://nope"}
	cfg.ApplyDefaults()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	p, err := pool.Open(ctx, cfg)
	if err == nil {
		if p != nil {
			p.Close()
		}
		t.Fatal("expected error from bad URL")
	}
}
