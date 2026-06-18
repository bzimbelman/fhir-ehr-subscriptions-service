// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package loader_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/loader"
)

// S-15 #2: env-var derivation MUST detect collisions between distinct
// config paths so the operator does not get a silent override.
func TestEnvCollisions_DotIndexVsUnderscore(t *testing.T) {
	t.Parallel()

	known := []string{
		"auth.trusted_issuers.0.jwks_url",
		"auth.trusted_issuers_0.jwks_url",
		"unrelated.value",
	}
	got := loader.EnvCollisions(known)
	sort.Strings(got)

	want := []string{"AUTH_TRUSTED_ISSUERS_0_JWKS_URL"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EnvCollisions = %v, want %v", got, want)
	}
}

func TestEnvCollisions_NoneWhenAllUnique(t *testing.T) {
	t.Parallel()
	known := []string{"a.b", "a.c", "x.y"}
	if got := loader.EnvCollisions(known); len(got) != 0 {
		t.Fatalf("expected no collisions, got %v", got)
	}
}

// Colliding envs are dropped (rather than picking a winner) when the
// var is set.
func TestReadEnvForKnownKeys_DropsCollidingEnvs(t *testing.T) {
	known := []string{
		"auth.trusted_issuers.0.jwks_url",
		"auth.trusted_issuers_0.jwks_url",
	}
	t.Setenv("AUTH_TRUSTED_ISSUERS_0_JWKS_URL", "https://idp.example/jwks")

	got := loader.ReadEnvForKnownKeys(known)
	if len(got) != 0 {
		t.Fatalf("expected colliding env dropped; got tree %#v", got)
	}
}
