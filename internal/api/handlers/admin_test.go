// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
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

// ----------------------------------------------------------------------
// Story #92 — RED tests below.
//
// 1) MinAdminTokenBytes wire-up enforcement
// 2) Audit emission on every authenticated admin handler
// 3) Cross-tenant isolation for /admin/subscriptions
// 4) Auth-failure paths return 401 (audit-deny is a future concern; see TODO)
// 5) Strict limit parser: reject negative, leading-+, unicode digits, int64 overflow, non-digits
// 6) Admin-specific rate limiter (Deps.AdminRateLimit) — 429 when bucket exhausted
//
// These tests pin the behavior story #92 + audit findings 7, 117, 165, 166,
// 167, 182, 183 require. They will fail until Phase B implements the
// changes.

// auditEvents returns a snapshot copy of the captured audit events so a
// test can scan them without holding the recorder's lock.
func auditEvents(m *memAudit) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.events))
	copy(out, m.events)
	return out
}

// containsAuditAction returns true when any captured event has the given
// action prefix (e.g. "admin.topics.list"). The recorder formats events
// as "action|target|outcome" so a prefix scan is correct.
func containsAuditAction(events []string, actionPrefix string) bool {
	for _, ev := range events {
		if strings.HasPrefix(ev, actionPrefix+"|") {
			return true
		}
	}
	return false
}

// AC #2: short admin token MUST NOT mount admin routes — observable
// behavior is "the chi router has no /admin/topics endpoint".
//
// Phase B is allowed to pick the wire-up mechanism (panic, error,
// silent-skip), so this test only asserts the observable outcome:
// requesting any /admin path against a router built with a short token
// returns 404 (route does not exist) or — if Phase B chose to panic —
// the panic is recovered and the request still 404s. Either way: no 200,
// no 401 (which would imply the route mounted and auth ran).
func TestRegisterAdminRoutes_RejectsShortToken_BelowMin(t *testing.T) {
	t.Parallel()

	// Try a few token lengths just under MinAdminTokenBytes.
	cases := []struct {
		name  string
		token string
	}{
		{"empty_treated_as_disabled_by_legacy_check", ""}, // existing behavior — disable
		{"one_byte", "x"},
		{"thirty_one_bytes", strings.Repeat("a", handlers.MinAdminTokenBytes-1)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := defaultDeps(t)
			deps.AdminToken = tc.token

			r := chi.NewRouter()
			// Phase B may choose to panic on short-non-empty tokens; we
			// catch the panic here so the test fails on missing
			// enforcement, not on the chosen mechanism.
			func() {
				defer func() { _ = recover() }()
				handlers.RegisterAdminRoutes(r, deps)
			}()

			srv := httptest.NewServer(r)
			defer srv.Close()

			// Bearer-token that would normally pass — irrelevant if the
			// route was never mounted.
			resp, _ := adminGet(t, srv, "/admin/topics", strings.Repeat("a", handlers.MinAdminTokenBytes))
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("token %q (%d bytes) MUST NOT mount admin routes; got 200", tc.token, len(tc.token))
			}
			// 404 (route absent) is the expected outcome for the legacy
			// empty-token check + the new short-token check. 401 would
			// mean the route mounted and auth ran — which is forbidden
			// for a token under the floor.
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("token %q (%d bytes): status = %d, want 404 (route should not be mounted)",
					tc.token, len(tc.token), resp.StatusCode)
			}
		})
	}
}

// AC #2: a token at or above MinAdminTokenBytes MUST mount admin routes.
// Pairs with the short-token test above so a regression that disables
// the surface entirely is caught.
func TestRegisterAdminRoutes_AcceptsTokenAtMin(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.AdminToken = strings.Repeat("a", handlers.MinAdminTokenBytes) // exactly 32 bytes
	srv := newAdminTestServer(t, deps)
	resp, _ := adminGet(t, srv, "/admin/topics", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 — token at MinAdminTokenBytes should mount routes", resp.StatusCode)
	}
}

// AC #3: every authenticated admin handler MUST emit an audit_log row
// before responding. Action is the admin operation; outcome="success" on
// the happy path. memAudit captures these via the existing
// AuditStore.Append helper.
func TestAdminHandlers_EmitAuditOnSuccess(t *testing.T) {
	t.Parallel()

	type tc struct {
		name        string
		path        string
		wantAction  string
		setupSubs   func(deps handlers.Deps) // optional: insert rows
		wantOutcome string
	}
	cases := []tc{
		{
			name:        "topics",
			path:        "/admin/topics",
			wantAction:  "admin.topics.list",
			wantOutcome: "success",
		},
		{
			name: "subscriptions",
			path: "/admin/subscriptions?clientId=client-A",
			setupSubs: func(deps handlers.Deps) {
				subs := deps.Subscriptions.(*memSubs)
				_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
					ClientID:    "client-A",
					Status:      repos.SubActive,
					TopicURL:    "http://example.org/topics/orders",
					ChannelType: "rest-hook",
					Endpoint:    "https://example.org/wh",
				})
			},
			wantAction:  "admin.subscriptions.list",
			wantOutcome: "success",
		},
		{
			name:        "dead_letters",
			path:        "/admin/dead_letters",
			wantAction:  "admin.dead_letters.list",
			wantOutcome: "success",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			deps := adminTestDeps(t)
			if c.setupSubs != nil {
				c.setupSubs(deps)
			}
			audit := deps.Audit.(*memAudit)
			srv := newAdminTestServer(t, deps)

			resp, body := adminGet(t, srv, c.path, deps.AdminToken)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d body=%s", resp.StatusCode, body)
			}

			events := auditEvents(audit)
			if len(events) == 0 {
				t.Fatalf("no audit events recorded for %s; want at least one with action=%q", c.path, c.wantAction)
			}
			if !containsAuditAction(events, c.wantAction) {
				t.Errorf("audit events did not contain action=%q; got=%v", c.wantAction, events)
			}
			// Outcome should be "success" — find the matching event and check.
			found := false
			for _, ev := range events {
				if strings.HasPrefix(ev, c.wantAction+"|") {
					found = true
					if !strings.HasSuffix(ev, "|"+c.wantOutcome) {
						t.Errorf("event %q outcome != %q", ev, c.wantOutcome)
					}
				}
			}
			if !found {
				t.Errorf("no event with action prefix %q in %v", c.wantAction, events)
			}
		})
	}
}

// AC #3 (failure path): admin handler error paths still emit audit with
// outcome != "success". Easiest seam to exercise: /admin/subscriptions
// without clientId returns 400 (validation failure) — must still emit.
func TestAdminHandlers_EmitAuditOnValidationFailure(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	audit := deps.Audit.(*memAudit)
	srv := newAdminTestServer(t, deps)

	resp, _ := adminGet(t, srv, "/admin/subscriptions", deps.AdminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	events := auditEvents(audit)
	if !containsAuditAction(events, "admin.subscriptions.list") {
		t.Errorf("validation failure path did not emit audit; events=%v", events)
	}
	// Outcome should NOT be "success" on a validation failure.
	for _, ev := range events {
		if strings.HasPrefix(ev, "admin.subscriptions.list|") && strings.HasSuffix(ev, "|success") {
			t.Errorf("validation failure recorded as success: %q", ev)
		}
	}
}

// AC: cross-tenant isolation. /admin/subscriptions?clientId=client-A
// MUST NOT return rows owned by client-B even when both exist in the store.
func TestAdminListSubscriptions_CrossTenantIsolation(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	_, _ = subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh-A",
	})
	bID, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-B",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh-B",
	})

	srv := newAdminTestServer(t, deps)
	resp, body := adminGet(t, srv, "/admin/subscriptions?clientId=client-A", deps.AdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	items, _ := got["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1 (only client-A); body=%s", len(items), body)
	}
	first, _ := items[0].(map[string]any)
	if first["clientId"] != "client-A" {
		t.Errorf("returned row owned by %v, want client-A", first["clientId"])
	}
	// Belt-and-suspenders: the body must not mention client-B's row id.
	if strings.Contains(string(body), bID.String()) {
		t.Errorf("response leaked client-B subscription id %s; body=%s", bID, body)
	}
	if strings.Contains(string(body), "client-B") {
		t.Errorf("response leaked client-B identifier; body=%s", body)
	}
}

// AC: auth-failure paths. Each authenticated admin route returns 401 on
// missing/wrong auth.
//
// TODO(story #92, security followup): emitting an audit-log row with
// outcome="failure" / action="admin.auth.deny" on 401 is a separate
// concern. This test only pins the 401 status — Phase B may choose to
// add audit-deny if it lands cleanly, but the acceptance criterion does
// not require it (the criterion lists handler-level audit, not gate-level).
func TestAdminRoutes_AuthFailures_AllPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path string
	}{
		{"topics", "/admin/topics"},
		{"subscriptions", "/admin/subscriptions?clientId=client-A"},
		{"dead_letters", "/admin/dead_letters"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name+"_missing_auth", func(t *testing.T) {
			t.Parallel()
			deps := adminTestDeps(t)
			srv := newAdminTestServer(t, deps)
			resp, body := adminGet(t, srv, c.path, "")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s missing auth: status=%d, want 401; body=%s", c.path, resp.StatusCode, body)
			}
		})
		t.Run(c.name+"_wrong_auth", func(t *testing.T) {
			t.Parallel()
			deps := adminTestDeps(t)
			srv := newAdminTestServer(t, deps)
			resp, body := adminGet(t, srv, c.path, "wrong-token-of-sufficient-length-32xxx")
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s wrong auth: status=%d, want 401; body=%s", c.path, resp.StatusCode, body)
			}
		})
	}
}

// AC #5: strict limit parser. Each malformed input returns 400 with a
// JSON error body. Whitespace inside the value (after TrimSpace) is
// rejected.
func TestAdminListDeadLetters_StrictLimitParser_Rejects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"empty_after_question", ""},
		{"non_digits", "abc"},
		{"negative", "-1"},
		{"zero", "0"},
		{"leading_plus", "+42"},
		{"unicode_digits_devanagari", "४२"},
		{"int64_overflow_plus_one", "9223372036854775808"},
		{"int64_overflow_huge", "99999999999999999999"},
		{"internal_whitespace", "4 2"},
		{"trailing_garbage", "42x"},
		{"hex_form", "0x10"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			deps := adminTestDeps(t)
			srv := newAdminTestServer(t, deps)

			// URL-encode the raw value so non-ASCII / spaces survive transport.
			path := "/admin/dead_letters?limit=" + escapeQueryValue(c.raw)
			resp, body := adminGet(t, srv, path, deps.AdminToken)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("limit=%q: status=%d, want 400; body=%s", c.raw, resp.StatusCode, body)
			}
			// Body must be a JSON object with an error field.
			var got map[string]any
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("limit=%q: response is not JSON: %v body=%s", c.raw, err, body)
			}
			if got["error"] == nil {
				t.Errorf("limit=%q: response JSON missing 'error' field; body=%s", c.raw, body)
			}
		})
	}
}

// AC #5 (positive): valid limits — including ones that exceed
// MaxAdminDeadLetterLimit — succeed (clamped, not rejected).
func TestAdminListDeadLetters_StrictLimitParser_Accepts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		raw       string
		wantLimit int
	}{
		{"one", "1", 1},
		{"max", fmt.Sprintf("%d", handlers.MaxAdminDeadLetterLimit), handlers.MaxAdminDeadLetterLimit},
		{"above_max_clamps", fmt.Sprintf("%d", handlers.MaxAdminDeadLetterLimit+1), handlers.MaxAdminDeadLetterLimit},
		{"surrounding_whitespace_trimmed", "  42  ", 42},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			deps := adminTestDeps(t)
			srv := newAdminTestServer(t, deps)
			resp, body := adminGet(t, srv, "/admin/dead_letters?limit="+escapeQueryValue(c.raw), deps.AdminToken)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("limit=%q: status=%d, want 200; body=%s", c.raw, resp.StatusCode, body)
			}
			var got map[string]any
			_ = json.Unmarshal(body, &got)
			gotLimit, _ := got["limit"].(float64)
			if int(gotLimit) != c.wantLimit {
				t.Errorf("limit=%q: response limit=%v, want %d", c.raw, got["limit"], c.wantLimit)
			}
		})
	}
}

// AC #4: when Deps.AdminRateLimit is set, 429 is returned after the
// burst is exhausted. nil-safe: nil disables the limiter (already
// covered by the other tests which leave AdminRateLimit unset).
func TestAdminRoutes_RateLimit_Exhausted_429(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	deps.AdminRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           2,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) })

	srv := newAdminTestServer(t, deps)

	// Two requests should pass.
	for i := 0; i < 2; i++ {
		resp, body := adminGet(t, srv, "/admin/topics", deps.AdminToken)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: status=%d, want 200; body=%s", i+1, resp.StatusCode, body)
		}
	}
	// Third should hit 429.
	resp, body := adminGet(t, srv, "/admin/topics", deps.AdminToken)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third attempt: status=%d, want 429; body=%s", resp.StatusCode, body)
	}
}

// AC #4: nil-safe — leaving Deps.AdminRateLimit unset disables limiting.
// (Sanity check that the other tests' zero-value behavior is intentional.)
func TestAdminRoutes_RateLimit_NilDisables(t *testing.T) {
	t.Parallel()
	deps := adminTestDeps(t)
	deps.AdminRateLimit = nil
	srv := newAdminTestServer(t, deps)

	// Hammer the surface — none should 429.
	for i := 0; i < 20; i++ {
		resp, body := adminGet(t, srv, "/admin/topics", deps.AdminToken)
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: 429 with nil AdminRateLimit; body=%s", i+1, body)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d: status=%d, want 200; body=%s", i+1, resp.StatusCode, body)
		}
	}
}

// escapeQueryValue is a small helper so we can build query strings with
// non-ASCII / whitespace test inputs without pulling in net/url at every
// call site.
func escapeQueryValue(v string) string {
	// Inline the small subset of encoding rules we need: spaces become
	// %20, plus stays as %2B (so the "+42" case actually transmits with
	// a leading +, not a space), unicode bytes become %XX. Strict-ish.
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == ' ':
			b.WriteString("%20")
		case r == '+':
			b.WriteString("%2B")
		case r < 0x80 && (r == '-' || r == '_' || r == '.' || r == '~' ||
			(r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')):
			b.WriteRune(r)
		default:
			// Encode rune as UTF-8 percent-escapes.
			for _, by := range []byte(string(r)) {
				b.WriteString(fmt.Sprintf("%%%02X", by))
			}
		}
	}
	return b.String()
}
