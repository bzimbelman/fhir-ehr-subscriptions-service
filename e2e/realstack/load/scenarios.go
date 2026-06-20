// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package load

import (
	"net/http"
	"time"
)

// ScenarioSubscriptionCreate429 returns a Spec that drives 300 RPS at
// POST /Subscription for 30 seconds — the example shape pinned by
// OpenProject story #258 acceptance ("300 RPS sustained for 30s
// produces ≥45 responses with status 429 carrying real Retry-After
// headers"). Caller must boot the realstack with
// SubscriptionCreateRateLimit configured low enough to fire under
// this load (Burst=5, RefillPerSecond=1 is sufficient).
//
// Closes Findings #18 (rate limit not on wire), #34 (429 not emitted
// under saturation), and #35 (Retry-After missing) per
// docs/e2e-coverage-strategy.md §3.H3.
func ScenarioSubscriptionCreate429(targetURL, token string) Spec {
	return Spec{
		Name:      "subscription-create-429",
		TargetURL: targetURL,
		Method:    http.MethodPost,
		Body:      []byte(MinimalSubscription),
		Headers: http.Header{
			"Content-Type": []string{"application/fhir+json"},
		},
		Token:                  token,
		Rate:                   300,
		Duration:               30 * time.Second,
		CaptureResponseHeaders: true,
	}
}

// ScenarioWSBindingToken429 returns a Spec that drives 300 RPS at
// the $get-ws-binding-token operation. Used to assert the WS-binding
// token rate limiter is wired (Findings #100, #141).
func ScenarioWSBindingToken429(targetURL, token string) Spec {
	return Spec{
		Name:      "ws-binding-token-429",
		TargetURL: targetURL,
		Method:    http.MethodPost,
		Body:      []byte(`{"resourceType":"Parameters"}`),
		Headers: http.Header{
			"Content-Type": []string{"application/fhir+json"},
		},
		Token:                  token,
		Rate:                   300,
		Duration:               30 * time.Second,
		CaptureResponseHeaders: true,
	}
}

// ScenarioTokenEndpoint429 returns a Spec that drives the OAuth
// token endpoint at saturation to assert the per-source-IP token
// bucket is wired (Findings #167). Note: token endpoint is unauthed
// so Spec.Token is empty.
func ScenarioTokenEndpoint429(targetURL string) Spec {
	return Spec{
		Name:      "token-endpoint-429",
		TargetURL: targetURL,
		Method:    http.MethodPost,
		Body:      []byte("grant_type=client_credentials&client_id=loadtest&client_secret=loadtest"),
		Headers: http.Header{
			"Content-Type": []string{"application/x-www-form-urlencoded"},
		},
		Rate:                   300,
		Duration:               30 * time.Second,
		CaptureResponseHeaders: true,
	}
}

// MinimalSubscription is the smallest-possible R5 Subscription body
// the binary will accept far enough to traverse the auth +
// rate-limit middleware. The rate limiter sits in front of body
// validation so even an invalid Subscription is fine for these runs.
const MinimalSubscription = `{
  "resourceType": "Subscription",
  "status": "requested",
  "topic": "http://example.org/topics/encounter-create",
  "channelType": {"system": "http://terminology.hl7.org/CodeSystem/subscription-channel-type", "code": "rest-hook"},
  "endpoint": "http://example.com/cb"
}`
