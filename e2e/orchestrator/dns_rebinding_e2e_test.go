// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// flippingResolverE2E returns the first IP set on the first LookupIP
// call (subscription create-time validation) and the second IP set on
// every subsequent call (delivery-time re-validation). This drives the
// DNS-rebinding adversary on a real binary running real Postgres,
// real submatcher claim loops, real scheduler dispatch, and the real
// rest-hook channel constructed via resthook.New (no production stubs
// or fakes).
type flippingResolverE2E struct {
	calls atomic.Int64
	first []net.IP
	rest  []net.IP
}

func (r *flippingResolverE2E) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	n := r.calls.Add(1)
	if n == 1 {
		return r.first, nil
	}
	return r.rest, nil
}

// TestScenario_dns_rebinding (OP #182) drives the production
// rest-hook channel via the orchestrator harness, registering a
// hostname whose DNS resolution flips from a public IP at create time
// to an RFC1918 private IP before delivery. The channel must reject
// the delivery without dialing the subscriber, and the deliveries row
// must transition to a permanent failure state (dead-letter) — not
// silently succeed and not get scheduled forever.
func TestScenario_dns_rebinding(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 90*time.Second)
	defer cancel()

	resolver := &flippingResolverE2E{
		first: []net.IP{net.ParseIP("93.184.216.34")},
		rest:  []net.IP{net.ParseIP("10.0.0.5")},
	}
	// AllowHTTP=true so an http:// endpoint can be registered without
	// having to spin up a real-cert TLS server for a host the resolver
	// will lie about. The SSRF policy still rejects private IPs at
	// resolution time — that is the policy under test.
	validator := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP: true,
		Resolver:  resolver,
	})

	tlsSrv, err := hpipe.StartTLSRestHookServer(h.MockSub.RestHook.Handler())
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })

	// Real production rest-hook channel, built with the same validator
	// the API uses at create-time. The HTTPClient is the harness's
	// TLS-trusting client so HTTPS endpoints would otherwise resolve
	// against the harness mock subscriber if the validator did not
	// reject them.
	restCh, err := resthook.New(resthook.Options{
		HTTPClient:   tlsSrv.Client(),
		URLValidator: validator,
	})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}
	t.Cleanup(func() { _ = restCh.Close() })

	fx := newScenarioFixture(t, ctx, h, scenarioConfig{
		preBuiltTLS: tlsSrv,
		pipelineConfig: hpipe.PipelineConfig{
			AdapterID: "default",
			Channels:  map[string]channel.Channel{"rest-hook": restCh},
		},
		topics: []hpipe.TopicFixture{{
			URL:     "http://example.org/topics/hl7-passthrough",
			Version: "1.0.0",
			Title:   "HL7 passthrough",
			Body:    []byte(passthroughTopicJSON),
		}},
	})

	tag := shortTag("dns-rebind")
	// Pre-warm the validator so the first LookupIP call returns the
	// public IP — modelling the API layer's create-time validation
	// accepting the hostname. The subsequent delivery-time call hits
	// the second answer (private 10.x). The harness's API does not run
	// URLValidator (no validator passed via APIServerConfig), so the
	// only Validate call against this resolver from production code
	// will be from the rest-hook channel at delivery time.
	if err := validator.Validate(ctx, "http://flipper.example.com/hook/"+tag); err != nil {
		t.Fatalf("create-time Validate must accept public IP: %v", err)
	}

	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", "http://flipper.example.com", tag, nil))

	driveAdmit(t, ctx, h, "DNS-REBIND-1-"+tag, "MRN-DNS-1", "A01")

	// Poll the deliveries row for this subscription until the
	// scheduler classifies it as dead. The rest-hook channel's
	// PermanentFailure ("ssrf policy: ...") should drive the
	// scheduler's ActionDeadLetter path within a few worker ticks.
	deadline := time.Now().Add(60 * time.Second)
	var status, lastErr string
	for time.Now().Before(deadline) {
		row := h.DB.QueryRow(ctx, `
			SELECT status, COALESCE(last_error, '')
			FROM deliveries
			WHERE subscription_id = $1
			ORDER BY created_at DESC
			LIMIT 1`, subID)
		if err := row.Scan(&status, &lastErr); err == nil && status == "dead" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if status != "dead" {
		dumpAndFail(t, ctx, h, subID, "expected deliveries.status='dead' from SSRF rejection at delivery time, got status=%q last_error=%q", status, lastErr)
	}
	low := strings.ToLower(lastErr)
	if !strings.Contains(low, "ssrf") && !strings.Contains(low, "blocked") &&
		!strings.Contains(low, "private") && !strings.Contains(low, "policy") {
		t.Errorf("expected SSRF-shaped last_error, got %q", lastErr)
	}
	// Validator must have been called more than once (create + at
	// least one delivery attempt). If only the create-time call
	// happened, the channel skipped the re-check.
	if got := resolver.calls.Load(); got < 2 {
		t.Errorf("expected validator to be invoked at delivery time (>=2 LookupIP calls), got %d", got)
	}
}
