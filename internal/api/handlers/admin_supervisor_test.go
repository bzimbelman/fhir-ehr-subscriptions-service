// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// Story #99 RED — these tests cover the /admin/supervisor/status
// endpoint contract. Phase B introduces:
//
//   - handlers.SupervisorStatusReader interface (Status() []supervisor.Status);
//   - Deps.SupervisorStatus field of that interface type;
//   - GET /admin/supervisor/status route mounted by RegisterAdminRoutes
//     when SupervisorStatus is non-nil, gated by the same shared-secret
//     middleware as the other admin endpoints.

// fakeSupervisorReader is the in-memory test double that returns a
// fixed set of supervisor.Status snapshots so the handler test does
// not depend on the supervisor goroutine machinery.
type fakeSupervisorReader struct {
	rows []supervisor.Status
}

func (f *fakeSupervisorReader) Status() []supervisor.Status { return f.rows }

func supervisorAdminDeps(t *testing.T, rows []supervisor.Status) handlers.Deps {
	t.Helper()
	d := adminTestDeps(t)
	d.SupervisorStatus = &fakeSupervisorReader{rows: rows}
	return d
}

// TestAdminSupervisorStatus_RequiresBearerToken asserts the route is
// mounted under the shared-secret gate.
func TestAdminSupervisorStatus_RequiresBearerToken(t *testing.T) {
	t.Parallel()

	deps := supervisorAdminDeps(t, nil)
	srv := newAdminTestServer(t, deps)

	resp, _ := adminGet(t, srv, "/admin/supervisor/status", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", resp.StatusCode)
	}

	resp, _ = adminGet(t, srv, "/admin/supervisor/status", "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", resp.StatusCode)
	}
}

// TestAdminSupervisorStatus_ReturnsAllSupervisors asserts the happy
// path renders every supervisor row with its adapter id, state,
// restart count, and last-error metadata.
func TestAdminSupervisorStatus_ReturnsAllSupervisors(t *testing.T) {
	t.Parallel()

	now := time.Now()
	rows := []supervisor.Status{
		{
			AdapterID:    "hl7-processor",
			State:        supervisor.StateRunning,
			RestartCount: 0,
		},
		{
			AdapterID:    "matcher",
			State:        supervisor.StateRestarting,
			RestartCount: 3,
			LastErrorAt:  now,
		},
		{
			AdapterID:    "submatcher",
			State:        supervisor.StateRunning,
			RestartCount: 1,
		},
		{
			AdapterID:    "scheduler",
			State:        supervisor.StateRunning,
			RestartCount: 0,
		},
	}
	deps := supervisorAdminDeps(t, rows)
	srv := newAdminTestServer(t, deps)

	resp, body := adminGet(t, srv, "/admin/supervisor/status", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d body=%s", resp.StatusCode, string(body))
	}

	var got struct {
		Total int              `json:"total"`
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, string(body))
	}
	if got.Total != 4 {
		t.Fatalf("total: want 4, got %d", got.Total)
	}
	if len(got.Items) != 4 {
		t.Fatalf("items: want 4, got %d", len(got.Items))
	}

	byID := make(map[string]map[string]any, 4)
	for _, item := range got.Items {
		id, _ := item["adapterId"].(string)
		byID[id] = item
	}
	for _, want := range []string{"hl7-processor", "matcher", "submatcher", "scheduler"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("missing supervisor %q in response: %+v", want, byID)
		}
	}
	matcher := byID["matcher"]
	if matcher["state"] != "restarting" {
		t.Errorf("matcher state: want restarting, got %v", matcher["state"])
	}
	if rc, _ := matcher["restartCount"].(float64); rc != 3 {
		t.Errorf("matcher restartCount: want 3, got %v", matcher["restartCount"])
	}
}

// TestAdminSupervisorStatus_DisabledWhenReaderNil asserts the endpoint
// is NOT mounted when SupervisorStatus is unwired. This keeps the
// admin surface honest: a probe should not learn about a stack that
// is not present.
func TestAdminSupervisorStatus_DisabledWhenReaderNil(t *testing.T) {
	t.Parallel()

	deps := adminTestDeps(t)
	deps.SupervisorStatus = nil
	srv := newAdminTestServer(t, deps)

	resp, _ := adminGet(t, srv, "/admin/supervisor/status", deps.AdminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled: want 404, got %d", resp.StatusCode)
	}
}

// TestAdminSupervisorStatus_EmitsAudit asserts every successful read
// of the supervisor status endpoint writes a row to audit_log so a
// post-incident triage can attribute who polled the supervisors.
//
// The admin tests share an in-memory audit store wired via adminTestDeps;
// this test relies on it being present.
func TestAdminSupervisorStatus_EmitsAudit(t *testing.T) {
	t.Parallel()

	deps := supervisorAdminDeps(t, nil)
	captured := newCapturingAuditStore()
	deps.Audit = captured
	srv := newAdminTestServer(t, deps)

	resp, _ := adminGet(t, srv, "/admin/supervisor/status", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}

	if got := captured.Calls(); got == 0 {
		t.Fatalf("expected audit Append for /admin/supervisor/status; got 0")
	}
	last := captured.Last()
	if last.Action != "admin.supervisor.status.list" {
		t.Errorf("audit action: want admin.supervisor.status.list, got %q", last.Action)
	}
	if last.Outcome != "success" {
		t.Errorf("audit outcome: want success, got %q", last.Outcome)
	}
}

// capturingAuditStore is a slim in-test AuditStore so the audit
// emission assertion is independent of the production audit chain.
type capturingAuditStore struct {
	last  capturedAudit
	count int
}

type capturedAudit struct {
	Action    string
	Target    string
	Outcome   string
	Canonical []byte
}

func newCapturingAuditStore() *capturingAuditStore { return &capturingAuditStore{} }

func (c *capturingAuditStore) Append(_ context.Context, action, target, outcome string, _ *uuid.UUID, canonical []byte) error {
	c.count++
	c.last = capturedAudit{Action: action, Target: target, Outcome: outcome, Canonical: append([]byte(nil), canonical...)}
	return nil
}
func (c *capturingAuditStore) Calls() int          { return c.count }
func (c *capturingAuditStore) Last() capturedAudit { return c.last }
