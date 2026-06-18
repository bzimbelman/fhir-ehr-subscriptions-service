// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package schemas_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/schemas"
)

// TestRegistryDefaultIncludesCoreDomains: the default registry ships the
// core domains listed in LLD §7.
func TestRegistryDefaultIncludesCoreDomains(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	want := []string{
		"deployment", "server", "lifecycle", "storage", "auth",
		"topics", "delivery", "observability", "mllp_listener",
	}
	for _, d := range want {
		if r.Get(d) == nil {
			t.Fatalf("core domain %q missing from default registry", d)
		}
	}
}

// TestRegisterDomainSchema: adapter / channel manifests register their schemas
// at runtime via Register.
func TestRegisterDomainSchema(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	custom := []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {"foo": {"type": "string"}},
		"required": ["foo"]
	}`)
	if err := r.Register("adapter.epic", custom); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Get("adapter.epic") == nil {
		t.Fatalf("registered schema missing")
	}
}

// TestRegisterRejectsBadJSON: a malformed schema is rejected at registration
// time (not deferred to validate-time).
func TestRegisterRejectsBadJSON(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	err := r.Register("bad", []byte(`{ not valid json`))
	if err == nil {
		t.Fatalf("expected error for malformed schema")
	}
}

// TestKnownPaths: the registry walks every registered schema and returns the
// flat list of dotted config paths the loader will mirror to env-var names.
//
// For "deployment" with properties facility_id, environment, log_level,
// log_format, the result includes "deployment.facility_id" etc. Array
// shapes generate path segments with explicit index 0 so env-var translation
// remains positional.
func TestKnownPaths(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	paths := r.KnownPaths()
	if len(paths) == 0 {
		t.Fatalf("KnownPaths returned empty")
	}
	must := []string{
		"deployment.facility_id",
		"deployment.log_level",
		"server.http.bind",
		"storage.postgres.url",
		"auth.trusted_issuers.0.issuer",
	}
	for _, m := range must {
		found := false
		for _, p := range paths {
			if p == m {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("KnownPaths missing %q. Got: %s", m, strings.Join(paths, ", "))
		}
	}
}

// TestSensitivePaths: walk the schema and surface every "sensitive: true"
// path. Schema authors annotate sensitive fields via the custom keyword.
func TestSensitivePaths(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	got := r.SensitivePaths()
	must := []string{
		"storage.postgres.url",
		"storage.encryption.at_rest_key",
	}
	for _, m := range must {
		found := false
		for _, p := range got {
			if p == m {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("SensitivePaths missing %q. Got: %v", m, got)
		}
	}
}

// TestReloadablePaths: the architecture commits a reloadable subset
// (LLD §8). Built-in domains contribute their own static reloadable paths;
// channel manifests register their additional reloadable_paths.
func TestReloadablePaths(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	rp := r.ReloadablePaths()
	wantPrefixes := []string{
		"topics.",
		"auth.client_registry",
		"deployment.log_level",
		"delivery.retry.",
	}
	for _, prefix := range wantPrefixes {
		hit := false
		for _, p := range rp {
			if p == prefix || strings.HasPrefix(p, prefix) {
				hit = true
				break
			}
		}
		if !hit {
			t.Fatalf("ReloadablePaths missing prefix %q; got %v", prefix, rp)
		}
	}
}

// TestRegisterChannelReloadablePaths: channel manifests can register
// additional reloadable_paths that ReloadablePaths surfaces.
func TestRegisterChannelReloadablePaths(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	r.RegisterReloadable([]string{"channels.kafka.topic_prefix", "channels.email.from"})
	rp := r.ReloadablePaths()
	want := map[string]bool{
		"channels.kafka.topic_prefix": false,
		"channels.email.from":         false,
	}
	for _, p := range rp {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Fatalf("RegisterReloadable: %q not surfaced", k)
		}
	}
}
