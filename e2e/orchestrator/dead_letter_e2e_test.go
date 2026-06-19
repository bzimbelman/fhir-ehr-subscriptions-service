// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package orchestrator

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/e2e/realstack"
)

// TestScenario_DeliveryDeadLetterAfterMax covers the engine + dead_letters
// table interaction: when delivery to a permanently-failing endpoint
// exhausts the retry budget, the engine MUST write a dead_letters row
// referencing the failed delivery so an operator can inspect it.
//
// The test points the Subscription at a non-routable endpoint
// (127.0.0.1:1 is reserved as a real-stack convention for "dead drop"),
// drives a resource_change, then queries the binary's real Postgres for
// a dead_letters row with the correct subscription id.
//
// Replaces the t.Skip stub previously in skipped_scenarios_test.go for
// OpenProject story #145.
func TestScenario_DeliveryDeadLetterAfterMax(t *testing.T) {
	s := bootForScenario(t, realstack.Options{})
	tag := shortTagFor(t)

	// Submit a Subscription whose endpoint refuses connections so the
	// engine exhausts its retry curve.
	body := buildSubscriptionWithEndpoint(
		"http://example.org/topics/service-request-scan-changed",
		"http://127.0.0.1:1/dead/"+tag)
	subID := s.postSubscription(body)

	s.hapiPostResource("ServiceRequest", map[string]any{
		"resourceType": "ServiceRequest",
		"id":           "dlq-1",
		"status":       "active",
		"intent":       "order",
		"subject":      map[string]any{"reference": "Patient/p-dlq"},
	})

	// Wait for the retry curve to exhaust. With the test config's
	// shrunken retry curve this should be inside 60s; we give 120s of
	// headroom.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if dlqHasRowForSub(t, s.ctx, s.stack.Postgres.URL, subID) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("DeliveryDeadLetterAfterMax: dead_letters has no row for subscription %s after 120s", subID)
}

// dlqHasRowForSub returns true when the engine has written a dead_letters
// row referencing the given subscription id. Uses the real Postgres URL
// the binary is wired against so the assertion exercises the production
// query path.
func dlqHasRowForSub(t *testing.T, ctx context.Context, dbURL, subID string) bool {
	t.Helper()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open dlq pg: %v", err)
	}
	defer db.Close()
	q := `SELECT 1 FROM dead_letters WHERE subscription_id = $1::uuid LIMIT 1`
	var n int
	err = db.QueryRowContext(ctx, q, subID).Scan(&n)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Logf("dlq query: %v", err)
		return false
	}
	return n == 1
}
