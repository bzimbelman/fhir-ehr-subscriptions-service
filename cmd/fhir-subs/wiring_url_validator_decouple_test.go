// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// staticResolverDecouple returns a deterministic IP set for any host.
// It is the same shape as handlers.staticResolver in the handlers test
// package; duplicated here so the cmd/fhir-subs tests don't depend on
// the test-only export.
type staticResolverDecouple struct {
	ips []net.IP
}

func (s staticResolverDecouple) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	return s.ips, nil
}

// writeTempYAML is provided by config_test.go in the same package.

// minimalConfigYAML returns the smallest YAML that loadConfig accepts
// without erroring, plus an additional `extra` block the caller writes
// in to exercise the field under test.
func minimalConfigYAML(extra string) string {
	return `
deployment:
  facility_id: f1
adapter:
  id: default
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
database:
  url: postgres://example.invalid/db?sslmode=disable
auth:
  audience: https://example.invalid
topics:
  catalog_dir: /tmp/topics
` + extra
}

// TestConfig_URLValidatorAllowHTTPDecoupledFromInsecureJWKS — OP #184
// RED. Today wiring.go line ~324 reads AllowHTTP from
// cfg.Auth.AllowInsecure (allow_insecure_jwks). Two unrelated trust
// decisions share one switch: an operator who opts in to insecure JWKS
// for a dev IDP also implicitly opts in to http:// rest-hook
// endpoints. The AC requires a dedicated cfg.URLValidator section
// whose AllowHTTP field is INDEPENDENT of cfg.Auth.AllowInsecureJWKS.
//
// This test loads a YAML where allow_insecure_jwks=true but
// url_validator.allow_http is absent (default false), then constructs
// a URLValidator from the new cfg.URLValidator section and asserts
// http:// is REJECTED. The test will fail to compile until Phase B
// adds cfg.URLValidator.
func TestConfig_URLValidatorAllowHTTPDecoupledFromInsecureJWKS(t *testing.T) {
	t.Parallel()

	yaml := minimalConfigYAML(`
auth:
  allow_insecure_jwks: true
url_validator:
  allow_http: false
`)
	path := writeTempYAML(t,yaml)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// The new cfg.URLValidator section MUST exist on Config and MUST
	// drive AllowHTTP independently of cfg.Auth.AllowInsecure. Phase B
	// adds the URLValidator field — until then this line fails to
	// compile, which is the RED.
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP:  cfg.URLValidator.AllowHTTP,
		AllowHosts: cfg.URLValidator.AllowHosts,
		Resolver:   staticResolverDecouple{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})

	if err := v.Validate(context.Background(), "http://example.com/hook"); err == nil {
		t.Fatalf("expected http rejection: AllowHTTP must be independent of allow_insecure_jwks")
	}
}

// TestConfig_URLValidatorAllowHTTPInverse covers the converse: HTTP is
// accepted when url_validator.allow_http=true regardless of the (off
// by default) auth.allow_insecure_jwks setting.
func TestConfig_URLValidatorAllowHTTPInverse(t *testing.T) {
	t.Parallel()

	yaml := minimalConfigYAML(`
url_validator:
  allow_http: true
`)
	path := writeTempYAML(t,yaml)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP:  cfg.URLValidator.AllowHTTP,
		AllowHosts: cfg.URLValidator.AllowHosts,
		Resolver:   staticResolverDecouple{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})
	if err := v.Validate(context.Background(), "http://example.com/hook"); err != nil {
		t.Fatalf("expected http accepted with url_validator.allow_http=true; got %v", err)
	}
}

// TestConfig_URLValidatorAllowHostsFromConfig — OP #185 RED. The
// AllowHosts list MUST come from cfg.URLValidator.AllowHosts (the new
// home), not cfg.Auth.AllowSubscriberHosts. We load YAML that sets the
// new field to ["internal.corp"], inject a resolver that maps that
// host to RFC1918 10.0.0.5, and assert the validator allows it.
func TestConfig_URLValidatorAllowHostsFromConfig(t *testing.T) {
	t.Parallel()

	yaml := minimalConfigYAML(`
url_validator:
  allow_hosts:
    - internal.corp
`)
	path := writeTempYAML(t,yaml)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if len(cfg.URLValidator.AllowHosts) != 1 || cfg.URLValidator.AllowHosts[0] != "internal.corp" {
		t.Fatalf("cfg.URLValidator.AllowHosts = %v, want [internal.corp]", cfg.URLValidator.AllowHosts)
	}

	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHosts: cfg.URLValidator.AllowHosts,
		Resolver:   staticResolverDecouple{ips: []net.IP{net.ParseIP("10.0.0.5")}},
	})
	if err := v.Validate(context.Background(), "https://internal.corp/hook"); err != nil {
		t.Fatalf("expected internal.corp allowed via cfg.URLValidator.AllowHosts; got %v", err)
	}
}
