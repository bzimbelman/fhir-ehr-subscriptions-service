// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// Story #104: per-client rate limiters on POST /Subscription and on
// $get-ws-binding-token must be wired into Deps from cfg.Auth.* at
// startup. Today the config block parses cleanly but cmd/fhir-subs/
// wiring.go never calls auth.NewClientRateLimiter for these two
// fields, so the chi middleware degrades to pass-through (S-3.3).
//
// These tests fail until Phase B teaches:
//   - applySets() about subscription_create_rate_limit.* and
//     ws_binding_token_rate_limit.* keys, AND
//   - buildProductionRuntime() to call NewClientRateLimiter for both
//     fields when configured.

// TestApplySets_AcceptsSubscriptionCreateRateLimitKeys is purely
// structural: it asserts the --set CLI surface accepts every key the
// operator needs to dial in the per-client limit on POST /Subscription.
// Runs without a database; complements the wiring helper test below.
func TestApplySets_AcceptsSubscriptionCreateRateLimitKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key string
		val string
	}{
		{"auth.subscription_create_rate_limit.burst", "10"},
		{"auth.subscription_create_rate_limit.refill_per_second", "1.5"},
		{"auth.subscription_create_rate_limit.max_keys", "1024"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("Config does not support --set %s — Phase B must teach config.go "+
					"about the auth.subscription_create_rate_limit.* keys: %v", c.key, err)
			}
		})
	}
}

// TestApplySets_AcceptsWSBindingTokenRateLimitKeys mirrors the
// SubscriptionCreate test for the WS bind-token mint surface.
func TestApplySets_AcceptsWSBindingTokenRateLimitKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		key string
		val string
	}{
		{"auth.ws_binding_token_rate_limit.burst", "20"},
		{"auth.ws_binding_token_rate_limit.refill_per_second", "2.0"},
		{"auth.ws_binding_token_rate_limit.max_keys", "2048"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.key, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			if err := applySets(cfg, []string{c.key + "=" + c.val}); err != nil {
				t.Fatalf("Config does not support --set %s — Phase B must teach config.go "+
					"about the auth.ws_binding_token_rate_limit.* keys: %v", c.key, err)
			}
		})
	}
}

// TestApplySets_RateLimitParseErrorRedacted asserts that a malformed
// rate-limit value is rejected with an opaque error (no value echoed),
// matching setParseErr behavior already tested for other --set keys.
func TestApplySets_RateLimitParseErrorRedacted(t *testing.T) {
	t.Parallel()

	cases := []string{
		"auth.subscription_create_rate_limit.burst=not-a-number",
		"auth.subscription_create_rate_limit.refill_per_second=not-a-float",
		"auth.subscription_create_rate_limit.max_keys=not-a-number",
		"auth.ws_binding_token_rate_limit.burst=not-a-number",
		"auth.ws_binding_token_rate_limit.refill_per_second=not-a-float",
		"auth.ws_binding_token_rate_limit.max_keys=not-a-number",
	}
	for _, set := range cases {
		set := set
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			err := applySets(cfg, []string{set})
			if err == nil {
				t.Fatalf("expected error on bad --set %q", set)
			}
			if strings.Contains(err.Error(), "not-a-number") || strings.Contains(err.Error(), "not-a-float") {
				t.Fatalf("error leaks operator-supplied value (S-1.1): %v", err)
			}
		})
	}
}

// TestNewClientRateLimitersFromConfig_PopulatesBothFields is the
// primary RED assertion: a small wiring helper must construct
// *auth.ClientRateLimiter for both story #104 surfaces from the typed
// config so cmd/fhir-subs/wiring.go just assigns the results into
// deps. The helper is the smallest seam where the unit test can prove
// the wiring doesn't silently drop a configured limiter.
func TestNewClientRateLimitersFromConfig_PopulatesBothFields(t *testing.T) {
	t.Parallel()

	cfg := &AuthConfig{
		SubscriptionCreateRateLimit: RateLimitConfig{
			Burst:           5,
			RefillPerSecond: 1.0,
			MaxKeys:         128,
		},
		WSBindingTokenRateLimit: RateLimitConfig{
			Burst:           7,
			RefillPerSecond: 2.0,
			MaxKeys:         256,
		},
	}

	got := buildClientRateLimitersFromAuth(cfg, nil)

	if got.SubscriptionCreate == nil {
		t.Fatalf("SubscriptionCreateRateLimit nil — wiring failed to call NewClientRateLimiter " +
			"(story #104 AC #1)")
	}
	if got.WSBindingToken == nil {
		t.Fatalf("WSBindingTokenRateLimit nil — wiring failed to call NewClientRateLimiter " +
			"(story #104 AC #1)")
	}
}

// TestNewClientRateLimitersFromConfig_NilSafeWhenDisabled asserts a
// zero RateLimitConfig (Burst <= 0) yields nil — both
// auth.NewClientRateLimiter and the chi Middleware contract treat nil
// as "no-op pass-through" so operators who omit the block keep
// unbounded behavior.
func TestNewClientRateLimitersFromConfig_NilSafeWhenDisabled(t *testing.T) {
	t.Parallel()

	cfg := &AuthConfig{} // both rate-limit blocks zero-valued

	got := buildClientRateLimitersFromAuth(cfg, nil)

	if got.SubscriptionCreate != nil {
		t.Fatalf("SubscriptionCreateRateLimit should be nil when Burst<=0; got %T", got.SubscriptionCreate)
	}
	if got.WSBindingToken != nil {
		t.Fatalf("WSBindingTokenRateLimit should be nil when Burst<=0; got %T", got.WSBindingToken)
	}
}

// TestNewClientRateLimitersFromConfig_TypeContract asserts the helper
// returns the public auth type so the production deps assignment
// compiles. Pinning the type keeps the seam hardcode-free: future
// refactors can't quietly swap in an internal type that bypasses the
// Middleware nil-safety contract.
func TestNewClientRateLimitersFromConfig_TypeContract(t *testing.T) {
	t.Parallel()

	cfg := &AuthConfig{
		SubscriptionCreateRateLimit: RateLimitConfig{Burst: 1, MaxKeys: 7},
		WSBindingTokenRateLimit:     RateLimitConfig{Burst: 1, MaxKeys: 7},
	}
	got := buildClientRateLimitersFromAuth(cfg, nil)
	var _ *auth.ClientRateLimiter = got.SubscriptionCreate
	var _ *auth.ClientRateLimiter = got.WSBindingToken
}
