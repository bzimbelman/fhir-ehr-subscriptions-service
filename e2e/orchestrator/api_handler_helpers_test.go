// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// seedHL7Topic seeds the same topic the rest of the orchestrator e2e
// scenarios rely on. The API-handler tests don't run a full pipeline
// (they care about the HTTP-boundary contract, not delivery), so they
// drop straight into subscription_topics here.
func seedHL7Topic(ctx context.Context, pool *pgxpool.Pool) error {
	const body = `{
	  "resourceType": "SubscriptionTopic",
	  "url": "http://example.org/topics/hl7-passthrough",
	  "version": "1.0.0",
	  "title": "HL7 passthrough",
	  "status": "active",
	  "resourceTrigger": [{
	    "resource": "Bundle",
	    "supportedInteraction": ["create", "update", "delete"]
	  }]
	}`
	const sql = `
		INSERT INTO subscription_topics
			(url, version, title, status, source, body, compiled_form)
		VALUES ($1, $2, $3, 'active', 'builtin', $4::jsonb, $5::bytea)
		ON CONFLICT (url, version) DO UPDATE
		  SET status = 'active',
		      body   = EXCLUDED.body`
	_, err := pool.Exec(ctx, sql,
		"http://example.org/topics/hl7-passthrough",
		"1.0.0",
		"HL7 passthrough",
		body,
		[]byte(body),
	)
	return err
}

// getSubRow loads a subscription row directly from the DB so e2e tests
// can assert on the API-mutated state.
func getSubRow(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*repos.SubscriptionRow, error) {
	const sql = `SELECT id, client_id, status, error
	             FROM subscriptions WHERE id = $1`
	row := &repos.SubscriptionRow{}
	var status, errMsg string
	if err := pool.QueryRow(ctx, sql, id).Scan(&row.ID, &row.ClientID, &status, &errMsg); err != nil {
		return nil, fmt.Errorf("get sub row %s: %w", id, err)
	}
	row.Status = repos.SubscriptionStatus(status)
	row.Error = errMsg
	return row, nil
}

// getAuditCanonicals returns every audit_log canonical_form row
// targeting the given subscription id, so tests can assert that the
// stored body has been redacted (B-13).
func getAuditCanonicals(ctx context.Context, pool *pgxpool.Pool, subID uuid.UUID) ([]string, error) {
	const sql = `SELECT canonical_form FROM audit_log WHERE target_id = $1 ORDER BY seq`
	rows, err := pool.Query(ctx, sql, subID.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, err
		}
		out = append(out, string(blob))
	}
	return out, rows.Err()
}

// panicMetrics is a MetricsRecorder that just counts each callback so
// the panic-recovery e2e test can observe RecordActivatePanic.
type panicMetrics struct {
	activatePanicTotal atomic.Int64
}

func (m *panicMetrics) RecordSubscriptionCreated()       {}
func (m *panicMetrics) RecordSubscriptionUpdated()       {}
func (m *panicMetrics) RecordSubscriptionDeleted()       {}
func (m *panicMetrics) RecordWSBindingTokenIssued()      {}
func (m *panicMetrics) RecordValidationFailure(_ string) {}
func (m *panicMetrics) RecordActivatePanic()             { m.activatePanicTotal.Add(1) }
func (m *panicMetrics) ActivatePanics() int64            { return m.activatePanicTotal.Load() }

// panickingActivator panics inside ActivateSubscription so the e2e
// test can verify the recover() path.
type panickingActivator struct{}

func (panickingActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	panic(fmt.Errorf("synthetic panic from %s", strings.Repeat("a", 1)))
}

// trackingChannel records every call to ActivateSubscription so tests
// asserting "the channel was never reached" (e.g., SSRF) have a
// counterfactual signal.
type trackingChannel struct {
	calls atomic.Int64
}

func (t *trackingChannel) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	t.calls.Add(1)
	return handlers.HandshakeSucceeded, nil
}

func (t *trackingChannel) Calls() int64 { return t.calls.Load() }

// Compile-time guard: panicMetrics implements both the base
// MetricsRecorder and the optional ActivatePanicRecorder extension.
var (
	_ handlers.MetricsRecorder       = (*panicMetrics)(nil)
	_ handlers.ActivatePanicRecorder = (*panicMetrics)(nil)
)

// keep imports tidy for the file boundary.
var _ = sync.WaitGroup{}
