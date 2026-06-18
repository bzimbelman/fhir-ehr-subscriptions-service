// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// memDeadLetters is an in-memory DeadLettersListStore for the admin tests.
type memDeadLetters struct {
	rows []repos.DeadLetterRow
}

func (m *memDeadLetters) ListRecent(_ context.Context, limit int) ([]repos.DeadLetterRow, error) {
	if limit <= 0 || limit > len(m.rows) {
		return m.rows, nil
	}
	return m.rows[:limit], nil
}

// newAdminTestServer wires a chi router with the admin routes ONLY (no
// SMART auth middleware) so tests can hit /admin/* directly with a
// shared-secret header.
func newAdminTestServer(t *testing.T, deps handlers.Deps) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	handlers.RegisterAdminRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func adminTestDeps(t *testing.T) handlers.Deps {
	t.Helper()
	d := defaultDeps(t)
	d.AdminToken = "test-admin-shared-secret-must-be-32+chars-long"
	d.DeadLetters = &memDeadLetters{
		rows: []repos.DeadLetterRow{
			{
				ID:          uuid.New(),
				Kind:        "delivery_exhausted",
				SourceTable: "deliveries",
				SourceID:    uuid.New(),
				Reason:      "max attempts",
				CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
			},
			{
				ID:          uuid.New(),
				Kind:        "hl7_unparseable",
				SourceTable: "hl7_message_queue",
				SourceID:    uuid.New(),
				Reason:      "bad msh",
				CreatedAt:   time.Date(2026, 6, 18, 11, 0, 0, 0, time.UTC),
			},
		},
	}
	return d
}

func adminGet(t *testing.T, srv *httptest.Server, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, body
}

// P1.6: when AdminToken is empty, the admin surface is disabled — the
// routes do not exist and a request returns 404 from the chi router.
func TestRegisterAdminRoutes_DisabledWhenTokenEmpty(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.AdminToken = "" // explicitly disabled
	srv := newAdminTestServer(t, deps)
	resp, _ := adminGet(t, srv, "/admin/topics", "any-token")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (route should not exist)", resp.StatusCode)
	}
}

// Without Authorization header, /admin returns 401.
func TestAdminRoutes_RejectMissingAuth(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/topics", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", resp.StatusCode, body)
	}
}

// Wrong bearer token returns 401, NOT 404 (so a probe can't enumerate
// the route surface).
func TestAdminRoutes_RejectWrongAuth(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/topics", "wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; body=%s", resp.StatusCode, body)
	}
}

// /admin/topics returns the active topic catalog when the token matches.
func TestAdminListTopics(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/topics", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if total, _ := got["total"].(float64); total != 1 {
		t.Errorf("total = %v, want 1", got["total"])
	}
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["url"] != "http://example.org/topics/orders" {
		t.Errorf("first url = %v", first["url"])
	}
}

// /admin/subscriptions requires clientId; returns 400 when missing.
func TestAdminListSubscriptions_RequiresClientID(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, _ := adminGet(t, srv, "/admin/subscriptions", deps.AdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when clientId missing", resp.StatusCode)
	}
}

// /admin/subscriptions returns the rows for the given client.
func TestAdminListSubscriptions_Happy(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
	})
	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/subscriptions?clientId=client-A", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["clientId"] != "client-A" {
		t.Errorf("clientId = %v", got["clientId"])
	}
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	if first["id"] != id.String() {
		t.Errorf("first id = %v, want %s", first["id"], id)
	}
}

// /admin/dead_letters returns 503 when the dead-letters store is not
// wired (Deps.DeadLetters == nil).
func TestAdminListDeadLetters_UnavailableWhenStoreNil(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	deps.DeadLetters = nil
	srv := newAdminTestServer(t, deps)
	resp, _ := adminGet(t, srv, "/admin/dead_letters", deps.AdminToken)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

// /admin/dead_letters happy path returns the most recent rows; the
// payload_redacted blob is intentionally absent.
func TestAdminListDeadLetters_Happy(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/dead_letters", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "payload_redacted") {
		t.Errorf("response leaked payload_redacted: %s", body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if total, _ := got["total"].(float64); total != 2 {
		t.Errorf("total = %v, want 2", got["total"])
	}
}

// limit=invalid returns 400.
func TestAdminListDeadLetters_BadLimitRejected(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	srv := newAdminTestServer(t, deps)
	resp, _ := adminGet(t, srv, "/admin/dead_letters?limit=abc", deps.AdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
