// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/wsbindingcache"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// memWsTokensIdem is a richer in-memory WsBindingTokensStore that
// supports the (subscription_id, client_id) reuse lookup required by
// OP #241. The default memWsTokens used elsewhere only implements
// Insert; this one implements the extended store contract.
type memWsTokensIdem struct {
	mu   sync.Mutex
	rows []repos.WsBindingTokenRow
}

func newMemWsTokensIdem() *memWsTokensIdem {
	return &memWsTokensIdem{}
}

func (m *memWsTokensIdem) Insert(_ context.Context, row repos.WsBindingTokenRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, row)
	return nil
}

// FindUnexpiredBySubscriptionAndClient returns the most recently
// issued unexpired token for (subscriptionID, clientID) or nil if
// none. Implementations MUST treat expires_at as exclusive (strictly
// in the future relative to `now`).
func (m *memWsTokensIdem) FindUnexpiredBySubscriptionAndClient(
	_ context.Context, subscriptionID uuid.UUID, clientID string, now time.Time,
) (*repos.WsBindingTokenRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *repos.WsBindingTokenRow
	for i := range m.rows {
		r := m.rows[i]
		if r.SubscriptionID != subscriptionID || r.ClientID != clientID {
			continue
		}
		if !r.ExpiresAt.After(now) {
			continue
		}
		if best == nil || r.ExpiresAt.After(best.ExpiresAt) {
			best = &m.rows[i]
		}
	}
	return best, nil
}

// rowCount returns the number of inserted rows; tests use this to
// assert that the second mint did NOT insert a fresh row.
func (m *memWsTokensIdem) rowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

// TestGetWsBindingToken_Idempotent_WithinTTL: OP #241 AC.
// Issuing the same client twice within TTL must return the same
// token without inserting a new row.
func TestGetWsBindingToken_Idempotent_WithinTTL(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	wsTok := newMemWsTokensIdem()
	deps.WsTokens = wsTok
	cache := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 16, Now: deps.Now})
	t.Cleanup(cache.Close)
	deps.WsTokenCache = handlers.WrapWsBindingTokenCache(cache)

	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "websocket",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)

	first := mintToken(t, srv.URL, id)
	if wsTok.rowCount() != 1 {
		t.Fatalf("expected 1 inserted row after first mint; got %d", wsTok.rowCount())
	}

	second := mintToken(t, srv.URL, id)
	if first != second {
		t.Errorf("expected idempotent token reuse within TTL; got first=%q second=%q", first, second)
	}
	if wsTok.rowCount() != 1 {
		t.Errorf("expected reuse to NOT insert a new row; got %d rows", wsTok.rowCount())
	}
}

// TestGetWsBindingToken_FreshAfterExpiry: OP #241 AC.
// After the first token's expires_at lapses, the next mint MUST
// produce a fresh token.
func TestGetWsBindingToken_FreshAfterExpiry(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	wsTok := newMemWsTokensIdem()
	deps.WsTokens = wsTok
	deps.WSBindingTTL = 1 * time.Minute

	// Mutable clock so the test can advance past TTL.
	current := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	deps.Now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return current
	}
	cache := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 16, Now: deps.Now})
	t.Cleanup(cache.Close)
	deps.WsTokenCache = handlers.WrapWsBindingTokenCache(cache)

	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "websocket",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)

	first := mintToken(t, srv.URL, id)

	// Advance past the TTL so the first row is expired.
	clockMu.Lock()
	current = current.Add(2 * time.Minute)
	clockMu.Unlock()

	second := mintToken(t, srv.URL, id)
	if first == second {
		t.Errorf("expected a fresh token after TTL lapse; got identical token")
	}
	if wsTok.rowCount() != 2 {
		t.Errorf("expected 2 rows total (one per mint); got %d", wsTok.rowCount())
	}
}

// TestGetWsBindingToken_PerClientScoping: OP #241 AC.
// Two different clients of the same subscription must each get their
// own token; the lookup is scoped on (subscription_id, client_id),
// not subscription_id alone.
func TestGetWsBindingToken_PerClientScoping(t *testing.T) {
	t.Parallel()
	wsTok := newMemWsTokensIdem()

	// Subscription is owned by client-A; the test asserts that the
	// store-level lookup is per-client even when the handler limits
	// access to the owner. We exercise this directly at the store.
	subID := uuid.New()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	if err := wsTok.Insert(context.Background(), repos.WsBindingTokenRow{
		Token: "tok-a", SubscriptionID: subID, ClientID: "client-A",
		ExpiresAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("insert A: %v", err)
	}
	if err := wsTok.Insert(context.Background(), repos.WsBindingTokenRow{
		Token: "tok-b", SubscriptionID: subID, ClientID: "client-B",
		ExpiresAt: now.Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("insert B: %v", err)
	}

	a, err := wsTok.FindUnexpiredBySubscriptionAndClient(context.Background(), subID, "client-A", now)
	if err != nil || a == nil || a.Token != "tok-a" {
		t.Errorf("expected tok-a for client-A; got %+v err=%v", a, err)
	}
	b, err := wsTok.FindUnexpiredBySubscriptionAndClient(context.Background(), subID, "client-B", now)
	if err != nil || b == nil || b.Token != "tok-b" {
		t.Errorf("expected tok-b for client-B; got %+v err=%v", b, err)
	}
}

// TestGetWsBindingToken_ReuseEmitsReuseAuditAction: OP #241 AC.
// Reuse path must record `subscription.ws-binding-token.reuse` so
// operators can distinguish it from the fresh-mint
// `subscription.ws-binding-token.issue` action.
func TestGetWsBindingToken_ReuseEmitsReuseAuditAction(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	wsTok := newMemWsTokensIdem()
	deps.WsTokens = wsTok
	audit := &memAudit{}
	deps.Audit = audit
	cache := wsbindingcache.New(wsbindingcache.Options{MaxKeys: 16, Now: deps.Now})
	t.Cleanup(cache.Close)
	deps.WsTokenCache = handlers.WrapWsBindingTokenCache(cache)

	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "websocket",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)

	_ = mintToken(t, srv.URL, id)
	_ = mintToken(t, srv.URL, id)

	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.events) != 2 {
		t.Fatalf("want 2 audit events (issue + reuse); got %d: %v", len(audit.events), audit.events)
	}
	wantFirstPrefix := "subscription.ws-binding-token.issue|"
	wantSecondPrefix := "subscription.ws-binding-token.reuse|"
	if got := audit.events[0]; len(got) < len(wantFirstPrefix) || got[:len(wantFirstPrefix)] != wantFirstPrefix {
		t.Errorf("first event = %q; want issue", got)
	}
	if got := audit.events[1]; len(got) < len(wantSecondPrefix) || got[:len(wantSecondPrefix)] != wantSecondPrefix {
		t.Errorf("second event = %q; want reuse", got)
	}
}

func mintToken(t *testing.T, base string, id uuid.UUID) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, base+"/Subscription/"+id.String()+"/$get-ws-binding-token", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	params, _ := got["parameter"].([]any)
	for _, p := range params {
		m, _ := p.(map[string]any)
		if m["name"] == "token" {
			return m["valueString"].(string)
		}
	}
	t.Fatalf("token not in response: %s", body)
	return ""
}
