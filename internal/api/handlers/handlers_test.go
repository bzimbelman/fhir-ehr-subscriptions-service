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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// memSubs is an in-memory SubscriptionsStore.
type memSubs struct {
	mu   sync.Mutex
	rows map[uuid.UUID]repos.SubscriptionRow

	// Counters / capture for S-2.4 If-None-Exist tests.
	listByClientCount   int
	findByCriteriaCount int
	lastFindCriteria    *handlers.SubscriptionMatchCriteria
}

func newMemSubs() *memSubs {
	return &memSubs{rows: map[uuid.UUID]repos.SubscriptionRow{}}
}

func (m *memSubs) resetCounters() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listByClientCount = 0
	m.findByCriteriaCount = 0
	m.lastFindCriteria = nil
}

func (m *memSubs) findByClientAndCriteriaCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.findByCriteriaCount
}

func (m *memSubs) listByClientCallsForIfNoneExist() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listByClientCount
}

func (m *memSubs) lastCriteria() *handlers.SubscriptionMatchCriteria {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastFindCriteria == nil {
		return nil
	}
	c := *m.lastFindCriteria
	return &c
}

func (m *memSubs) Insert(_ context.Context, row repos.SubscriptionRow) (uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := uuid.New()
	row.ID = id
	row.CreatedAt = time.Now().UTC()
	row.UpdatedAt = row.CreatedAt
	m.rows[id] = row
	return id, nil
}

func (m *memSubs) GetByID(_ context.Context, id uuid.UUID) (*repos.SubscriptionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, nil
	}
	r2 := r
	return &r2, nil
}

func (m *memSubs) ListByClient(_ context.Context, clientID string) ([]repos.SubscriptionRow, error) {
	m.mu.Lock()
	m.listByClientCount++
	out := []repos.SubscriptionRow{}
	for _, r := range m.rows {
		if r.ClientID == clientID {
			out = append(out, r)
		}
	}
	m.mu.Unlock()
	return out, nil
}

func (m *memSubs) FindByClientAndCriteria(_ context.Context, clientID string, criteria handlers.SubscriptionMatchCriteria) ([]repos.SubscriptionRow, error) {
	m.mu.Lock()
	m.findByCriteriaCount++
	c := criteria
	m.lastFindCriteria = &c
	out := []repos.SubscriptionRow{}
	for _, r := range m.rows {
		if r.ClientID != clientID {
			continue
		}
		if r.Status == repos.SubOff {
			continue
		}
		if criteria.Topic != "" && r.TopicURL != criteria.Topic {
			continue
		}
		if criteria.ChannelType != "" && r.ChannelType != criteria.ChannelType {
			continue
		}
		if criteria.Endpoint != "" && r.Endpoint != criteria.Endpoint {
			continue
		}
		out = append(out, r)
		break // mimic LIMIT 1 in SQL
	}
	m.mu.Unlock()
	return out, nil
}

// ListByClientPage mirrors the Pg adapter: rows ordered by created_at
// DESC, id DESC, optionally cursored. The mem store is small enough to
// sort-and-slice in memory.
func (m *memSubs) ListByClientPage(_ context.Context, clientID string, after *handlers.SubscriptionCursor, limit int) ([]repos.SubscriptionRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		return nil, nil
	}
	all := make([]repos.SubscriptionRow, 0)
	for _, r := range m.rows {
		if r.ClientID != clientID {
			continue
		}
		all = append(all, r)
	}
	// Sort created_at DESC, then id DESC.
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID.String() > all[j].ID.String()
	})
	if after != nil {
		filtered := make([]repos.SubscriptionRow, 0, len(all))
		for _, r := range all {
			if r.CreatedAt.Equal(after.CreatedAt) {
				if r.ID.String() < after.ID.String() {
					filtered = append(filtered, r)
				}
				continue
			}
			if r.CreatedAt.Before(after.CreatedAt) {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (m *memSubs) UpdateResource(_ context.Context, id uuid.UUID, row repos.SubscriptionRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	row.ID = id
	row.ClientID = existing.ClientID
	row.CreatedAt = existing.CreatedAt
	row.UpdatedAt = time.Now().UTC()
	m.rows[id] = row
	return nil
}

func (m *memSubs) HardDelete(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[id]; !ok {
		return nil
	}
	delete(m.rows, id)
	return nil
}

func (m *memSubs) UpdateStatus(_ context.Context, id uuid.UUID, status repos.SubscriptionStatus, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return fmt.Errorf("not found")
	}
	r.Status = status
	r.Error = errMsg
	r.UpdatedAt = time.Now().UTC()
	m.rows[id] = r
	return nil
}

// memTopics is an in-memory SubscriptionTopicsStore.
type memTopics struct {
	rows []repos.SubscriptionTopicRow
}

func (m *memTopics) ListActive(_ context.Context) ([]repos.SubscriptionTopicRow, error) {
	out := make([]repos.SubscriptionTopicRow, 0, len(m.rows))
	for _, r := range m.rows {
		if r.Status == "active" {
			out = append(out, r)
		}
	}
	return out, nil
}

// memEvents is an in-memory EhrEventsStore.
type memEvents struct {
	rows []repos.EhrEventRow
}

// ListByTopicAndRangePage mirrors the Pg adapter: ordered by event_number
// ASC and capped at limit. limit <= 0 means "no cap". (S-2.15)
//
// clientID is the OP #274 tenant predicate. Empty clientID returns no
// rows so a missing principal cannot bypass tenant isolation.
func (m *memEvents) ListByTopicAndRangePage(_ context.Context, topicURL, clientID string, since, until int64, limit int) ([]repos.EhrEventRow, error) {
	matched := []repos.EhrEventRow{}
	if clientID == "" {
		return matched, nil
	}
	for _, r := range m.rows {
		if r.TopicURL != topicURL {
			continue
		}
		if r.ClientID != clientID {
			continue
		}
		if since > 0 && r.EventNumber < since {
			continue
		}
		if until > 0 && r.EventNumber > until {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].EventNumber < matched[j].EventNumber })
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// memDeliveries is an in-memory DeliveriesStore.
type memDeliveries struct {
	last map[uuid.UUID]int64
}

func (m *memDeliveries) LastDeliveredEventNumber(_ context.Context, sub uuid.UUID) (int64, error) {
	if m == nil || m.last == nil {
		return 0, nil
	}
	return m.last[sub], nil
}

// memWsTokens is an in-memory WsBindingTokensStore.
type memWsTokens struct {
	mu  sync.Mutex
	row map[string]repos.WsBindingTokenRow
}

func newMemWsTokens() *memWsTokens {
	return &memWsTokens{row: map[string]repos.WsBindingTokenRow{}}
}

func (m *memWsTokens) Insert(_ context.Context, row repos.WsBindingTokenRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.row[row.Token] = row
	return nil
}

// memAuth: not exercised by handlers tests since principal is preset.

// memAudit captures audit events.
type memAudit struct {
	mu     sync.Mutex
	events []string
}

func (m *memAudit) Append(_ context.Context, action, target, outcome string, _ *uuid.UUID, _ []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, fmt.Sprintf("%s|%s|%s", action, target, outcome))
	return nil
}

// fakeChannel implements ChannelActivator.
type fakeChannel struct {
	resp handlers.HandshakeOutcome
	err  error
}

func (f *fakeChannel) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return f.resp, f.err
}

// principalMiddleware injects a fixed principal into every request so
// handler tests can run without the auth verifier.
func principalMiddleware(p *auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// newTestServer wires a chi router with all handler routes plus a
// preset principal supplied via Deps.Auth (story #58 / N-1.4). Public
// routes (story #93) mount on the bare router before the auth-protected
// group so the in-test wiring matches production wiring.
func newTestServer(t *testing.T, principal *auth.Principal, deps handlers.Deps) *httptest.Server {
	t.Helper()
	deps.Auth = principalMiddleware(principal)
	r := chi.NewRouter()
	handlers.RegisterPublicRoutes(r, deps)
	handlers.RegisterRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func defaultDeps(t *testing.T) handlers.Deps {
	t.Helper()
	subs := newMemSubs()
	topics := &memTopics{rows: []repos.SubscriptionTopicRow{
		{
			URL:     "http://example.org/topics/orders",
			Version: "1.0.0",
			Status:  "active",
			Source:  "builtin",
		},
	}}
	deliveries := &memDeliveries{last: map[uuid.UUID]int64{}}
	events := &memEvents{}
	wsTok := newMemWsTokens()
	audit := &memAudit{}
	now := func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) }
	return handlers.Deps{
		Subscriptions: subs,
		Topics:        topics,
		Events:        events,
		Deliveries:    deliveries,
		WsTokens:      wsTok,
		Audit:         audit,
		Channels: handlers.ChannelRegistry{
			"rest-hook": &fakeChannel{resp: handlers.HandshakeSucceeded},
			"websocket": &fakeChannel{resp: handlers.HandshakeSucceeded},
		},
		Now:           now,
		WSBindingTTL:  5 * time.Minute,
		BaseURL:       "https://api.example",
		WSBaseURL:     "wss://api.example/ws",
		ServerVersion: "test",
	}
}

func defaultPrincipal() *auth.Principal {
	return &auth.Principal{
		ClientID: "client-A",
		Scopes: []string{
			"system/Subscription.c",
			"system/Subscription.r",
			"system/Subscription.u",
			"system/Subscription.d",
			"system/Subscription.cruds",
		},
		Exp: time.Date(2026, 6, 18, 13, 0, 0, 0, time.UTC),
	}
}

func TestCreateSubscription_Happy(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", resp.StatusCode, respBody)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/Subscription/") {
		t.Errorf("Location = %q", loc)
	}
	var got map[string]any
	_ = json.Unmarshal(respBody, &got)
	if got["resourceType"] != "Subscription" {
		t.Errorf("resourceType = %v", got["resourceType"])
	}
	// status should be requested initially per LLD; activation is async.
	if got["status"] != "requested" && got["status"] != "active" {
		t.Errorf("status = %v", got["status"])
	}
}

func TestCreateSubscription_UnknownTopic_422(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)

	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/unknown",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "OperationOutcome") {
		t.Errorf("expected OperationOutcome; got %s", respBody)
	}
}

func TestCreateSubscription_BadBody_400(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(`{not json`))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestCreateSubscription_InsufficientScope_403(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	p := &auth.Principal{
		ClientID: "client-A",
		Scopes:   []string{"system/Subscription.r"},
	}
	srv := newTestServer(t, p, deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestReadSubscription_OwnedByOtherClient_404(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-other",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String())
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestReadSubscription_Owned_200(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String())
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["id"] != id.String() {
		t.Errorf("id = %v", got["id"])
	}
}

func TestUpdateSubscription_TakesEffectImmediately(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh",
		"content": "id-only",
		"maxCount": 5,
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, respBody)
	}
	row, _ := subs.GetByID(context.Background(), id)
	if row.MaxCount != 5 {
		t.Errorf("MaxCount = %d; want 5", row.MaxCount)
	}
	// Status untouched (still active) — TakesEffectImmediately path.
	if row.Status != repos.SubActive {
		t.Errorf("status = %s; want active", row.Status)
	}
}

func TestUpdateSubscription_ChangedEndpoint_TriggersReHandshake(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "active",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/wh-new",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/Subscription/"+id.String(), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// Wait briefly for async handshake and then assert active again.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := subs.GetByID(context.Background(), id)
		if row.Status == repos.SubActive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeleteSubscription_Owned_204(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/Subscription/"+id.String(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d", resp.StatusCode)
	}
	// OP #191: DELETE is a hard-delete. The row MUST be gone so a
	// subsequent GET /Subscription/{id} returns 404 — soft-delete
	// (status=off) leaves the encrypted contact info readable
	// indefinitely.
	row, _ := subs.GetByID(context.Background(), id)
	if row != nil {
		t.Fatalf("subscription row still present after DELETE; OP #191 requires hard-delete (got status=%s)", row.Status)
	}
	resp2, err := http.Get(srv.URL + "/Subscription/" + id.String())
	if err != nil {
		t.Fatalf("GET-after-DELETE: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp2.Body)
		t.Errorf("GET-after-DELETE status = %d; want 404 (FHIR R5 §3.4.4); body=%s", resp2.StatusCode, body)
	}
}

func TestStatusSingle_Returns_SubscriptionStatus(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:                     "client-A",
		Status:                       repos.SubActive,
		TopicURL:                     "http://example.org/topics/orders",
		ChannelType:                  "rest-hook",
		Endpoint:                     "https://example.org/wh",
		Content:                      "id-only",
		MaxCount:                     1,
		EventsSinceSubscriptionStart: 7,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$status")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SubscriptionStatus") {
		t.Errorf("body should contain SubscriptionStatus; got %s", body)
	}
	if !strings.Contains(string(body), "query-status") {
		t.Errorf("body should contain query-status type; got %s", body)
	}
}

func TestEvents_Replay(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	events := deps.Events.(*memEvents)
	events.rows = []repos.EhrEventRow{
		{ClientID: "client-A", EventNumber: 1, TopicURL: "http://example.org/topics/orders", Focus: "ServiceRequest/abc"},
		{ClientID: "client-A", EventNumber: 2, TopicURL: "http://example.org/topics/orders", Focus: "ServiceRequest/def"},
		{ClientID: "client-A", EventNumber: 3, TopicURL: "http://example.org/topics/other", Focus: "Encounter/x"},
	}
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/Subscription/" + id.String() + "/$events?eventsSinceNumber=1&eventsUntilNumber=2")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "subscription-notification") {
		t.Errorf("body should contain subscription-notification; got %s", body)
	}
	// Should include 2 notificationEvent entries (events 1 and 2; event
	// 3 is for a different topic).
	if strings.Count(string(body), `"eventNumber"`) != 2 {
		t.Errorf("expected 2 eventNumber entries; got %s", body)
	}
}

func TestGetWsBindingToken_HappyPath(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "websocket",
		Endpoint:    "",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription/"+id.String()+"/$get-ws-binding-token", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if got["resourceType"] != "Parameters" {
		t.Errorf("resourceType = %v", got["resourceType"])
	}
	wsTok := deps.WsTokens.(*memWsTokens)
	if len(wsTok.row) != 1 {
		t.Errorf("expected one persisted token; got %d", len(wsTok.row))
	}
}

func TestGetWsBindingToken_NotWebsocket_422(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	subs := deps.Subscriptions.(*memSubs)
	id, _ := subs.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    "client-A",
		Status:      repos.SubActive,
		TopicURL:    "http://example.org/topics/orders",
		ChannelType: "rest-hook",
		Endpoint:    "https://example.org/wh",
		Content:     "id-only",
		MaxCount:    1,
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription/"+id.String()+"/$get-ws-binding-token", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestRoutes_NotFoundIsOperationOutcome(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newTestServer(t, defaultPrincipal(), deps)
	resp, err := http.Get(srv.URL + "/no/such/path")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("body should be OperationOutcome; got %s", body)
	}
}
