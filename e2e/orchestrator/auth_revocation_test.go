// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	hpipe "github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/harness"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel/resthook"
)

// TestScenario_auth_revocation: the subscriber returns 401 to every
// POST. The rest-hook channel classifies 401 as a permanent failure
// (per channels.md §4.1's mapping for non-408/429 4xx); the
// scheduler's classifier maps Permanent to dead-letter. Assert: a
// dead_letters row with subscription_id matching the test sub, and the
// delivery in 'dead' status with last_error mentioning 401.
func TestScenario_auth_revocation(t *testing.T) {
	h := requireHarness(t)
	ctx, cancel := newDeadline(context.Background(), 60*time.Second)
	defer cancel()

	tag := shortTag("auth-rev")
	denyAll := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hook/"+tag) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Other paths (control endpoints) pass through.
		h.MockSub.RestHook.Handler().ServeHTTP(w, r)
	})

	tlsSrv, err := hpipe.StartTLSRestHookServer(denyAll)
	if err != nil {
		t.Fatalf("tls rest-hook: %v", err)
	}
	t.Cleanup(func() { _ = tlsSrv.Close() })
	restCh, err := resthook.New(resthook.Options{HTTPClient: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("resthook.New: %v", err)
	}

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

	subID := fx.createSubscription(ctx, t, h,
		restHookSub("http://example.org/topics/hl7-passthrough", tlsSrv.URL, tag, nil))

	driveAdmit(t, ctx, h, "AUTH-1-"+tag, "MRN-AUTH", "A01")

	// Wait for the delivery to land in 'dead' status (dead-letter on
	// permanent failure).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		err := h.DB.QueryRow(ctx,
			`SELECT status FROM deliveries WHERE subscription_id=$1 ORDER BY created_at DESC LIMIT 1`,
			subID).Scan(&status)
		if err == nil && status == "dead" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	var status, lastErr string
	if err := h.DB.QueryRow(ctx,
		`SELECT status, last_error FROM deliveries
		 WHERE subscription_id=$1 ORDER BY created_at DESC LIMIT 1`,
		subID).Scan(&status, &lastErr); err != nil {
		dumpAndFail(t, ctx, h, subID, "read delivery row: %v", err)
	}
	if status != "dead" {
		dumpAndFail(t, ctx, h, subID, "delivery status: got %q want dead", status)
	}
	if !strings.Contains(lastErr, "401") {
		t.Errorf("delivery last_error: got %q, want substring 401", lastErr)
	}

	// Verify a dead_letters row was written for this delivery.
	var dlCount int
	if err := h.DB.QueryRow(ctx,
		`SELECT count(*) FROM dead_letters WHERE subscription_id=$1`, subID,
	).Scan(&dlCount); err != nil {
		dumpAndFail(t, ctx, h, subID, "count dead_letters: %v", err)
	}
	if dlCount < 1 {
		dumpAndFail(t, ctx, h, subID, "dead_letters row count: got %d, want >= 1", dlCount)
	}

	// And the journal stays empty for this tag — no successful POST.
	got := h.MockSub.RestHook.Received(tag)
	if len(got) != 0 {
		t.Errorf("expected 0 successful deliveries; got %d", len(got))
	}
	_ = uuid.Nil
}
