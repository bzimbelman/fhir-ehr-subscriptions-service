// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// S-3.3: per-client rate limit on POST /Subscription and on the
// $get-ws-binding-token operation. The auth package already exports a
// token-bucket primitive used on the public /token endpoint; this
// story plugs an instance into a chi middleware on the subscription
// create + WS bind-token mint routes so a single rogue client cannot
// starve others. Requests beyond the configured budget receive 429
// with a Retry-After hint.

const validCreateBody = `{
	"resourceType": "Subscription",
	"status": "requested",
	"topic": "http://example.org/topics/orders",
	"channelType": {"code": "rest-hook"},
	"endpoint": "https://example.org/webhook",
	"content": "id-only",
	"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
}`

func newRateLimitTestServer(t *testing.T, principal *auth.Principal, deps handlers.Deps) *httptest.Server {
	t.Helper()
	deps.Auth = principalMiddleware(principal)
	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func postCreate(t *testing.T, srv *httptest.Server) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(validCreateBody))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestCreateSubscription_RateLimit_UnderLimit_AllAccepted(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.SubscriptionCreateRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           5,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })
	srv := newRateLimitTestServer(t, defaultPrincipal(), deps)

	for i := 0; i < 5; i++ {
		resp := postCreate(t, srv)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("attempt %d: status=%d, want 201", i+1, resp.StatusCode)
		}
	}
}

func TestCreateSubscription_RateLimit_OverLimit_429(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.SubscriptionCreateRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           3,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })
	srv := newRateLimitTestServer(t, defaultPrincipal(), deps)

	for i := 0; i < 3; i++ {
		resp := postCreate(t, srv)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: rate-limited prematurely (code=%d)", i+1, resp.StatusCode)
		}
	}
	resp := postCreate(t, srv)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("4th attempt: status=%d, want 429; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatalf("4th attempt: missing Retry-After header")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "OperationOutcome") {
		t.Errorf("expected OperationOutcome body; got %s", body)
	}
}

func TestCreateSubscription_RateLimit_PerClientIsolation(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.SubscriptionCreateRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           1,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })

	deps.Auth = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id := req.Header.Get("X-Test-Client")
			if id == "" {
				id = "client-A"
			}
			p := *defaultPrincipal()
			p.ClientID = id
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), &p)))
		})
	}
	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	send := func(client string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/Subscription", strings.NewReader(validCreateBody))
		req.Header.Set("Content-Type", "application/fhir+json")
		req.Header.Set("X-Test-Client", client)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := send("client-A"); got != http.StatusCreated {
		t.Fatalf("client-A first request: got %d, want 201", got)
	}
	if got := send("client-B"); got != http.StatusCreated {
		t.Fatalf("client-B first request: got %d, want 201", got)
	}
	if got := send("client-A"); got != http.StatusTooManyRequests {
		t.Fatalf("client-A second: got %d, want 429", got)
	}
	if got := send("client-B"); got != http.StatusTooManyRequests {
		t.Fatalf("client-B second: got %d, want 429 (own bucket)", got)
	}
}

func TestCreateSubscription_RateLimit_ResetAfterWindow(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	clk := &advClock{now: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)}
	deps.SubscriptionCreateRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           1,
		RefillPerSecond: 1, // 1 token / second
	}, clk.Now)
	srv := newRateLimitTestServer(t, defaultPrincipal(), deps)

	resp := postCreate(t, srv)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first request: got %d, want 201", resp.StatusCode)
	}
	resp = postCreate(t, srv)
	retry := resp.Header.Get("Retry-After")
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", resp.StatusCode)
	}
	if retry == "" {
		t.Fatalf("expected Retry-After header on 429")
	}
	clk.Advance(2 * time.Second)
	resp = postCreate(t, srv)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("after-window request: got %d, want 201; retry=%s; body=%s", resp.StatusCode, retry, body)
	}
}

type advClock struct {
	now time.Time
}

func (c *advClock) Now() time.Time          { return c.now }
func (c *advClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// --- $get-ws-binding-token rate limit tests ---------------------------

func seedWsSubscription(t *testing.T, deps handlers.Deps, clientID string) uuid.UUID {
	t.Helper()
	ms, ok := deps.Subscriptions.(*memSubs)
	if !ok {
		t.Fatalf("Subscriptions is not *memSubs (got %T)", deps.Subscriptions)
	}
	id, err := ms.Insert(context.Background(), repos.SubscriptionRow{
		ClientID:    clientID,
		ChannelType: "websocket",
		TopicURL:    "http://example.org/topics/orders",
		Status:      "active",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return id
}

func postWsBindingToken(t *testing.T, srv *httptest.Server, id uuid.UUID) *http.Response {
	t.Helper()
	url := srv.URL + "/Subscription/" + id.String() + "/$get-ws-binding-token"
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

func TestGetWsBindingToken_RateLimit_OverLimit_429(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.WSBindingTokenRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           2,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })
	principal := defaultPrincipal()
	id := seedWsSubscription(t, deps, principal.ClientID)
	srv := newRateLimitTestServer(t, principal, deps)

	for i := 0; i < 2; i++ {
		resp := postWsBindingToken(t, srv, id)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("attempt %d: rate-limited prematurely (code=%d)", i+1, resp.StatusCode)
		}
	}
	resp := postWsBindingToken(t, srv, id)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("3rd attempt: status=%d, want 429; body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Retry-After"); got == "" {
		t.Fatalf("3rd attempt: missing Retry-After")
	} else if n, err := strconv.Atoi(got); err != nil || n < 1 {
		t.Fatalf("Retry-After=%q (parse=%v) want positive integer seconds", got, err)
	}
}

func TestGetWsBindingToken_RateLimit_PerClientIsolation(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.WSBindingTokenRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           1,
		RefillPerSecond: 0,
	}, func() time.Time { return time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC) })

	subA := seedWsSubscription(t, deps, "client-A")
	subB := seedWsSubscription(t, deps, "client-B")

	deps.Auth = func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id := req.Header.Get("X-Test-Client")
			if id == "" {
				id = "client-A"
			}
			p := *defaultPrincipal()
			p.ClientID = id
			next.ServeHTTP(w, req.WithContext(auth.WithPrincipal(req.Context(), &p)))
		})
	}
	r := chi.NewRouter()
	handlers.RegisterRoutes(r, deps)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	send := func(client string, subID uuid.UUID) int {
		url := srv.URL + "/Subscription/" + subID.String() + "/$get-ws-binding-token"
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(""))
		req.Header.Set("X-Test-Client", client)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := send("client-A", subA); got != http.StatusOK {
		t.Fatalf("client-A first: got %d, want 200", got)
	}
	if got := send("client-B", subB); got != http.StatusOK {
		t.Fatalf("client-B first: got %d, want 200", got)
	}
	if got := send("client-A", subA); got != http.StatusTooManyRequests {
		t.Fatalf("client-A second: got %d, want 429", got)
	}
	if got := send("client-B", subB); got != http.StatusTooManyRequests {
		t.Fatalf("client-B second: got %d, want 429 (own bucket)", got)
	}
}

func TestRateLimit_Disabled_PassesThrough(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	srv := newRateLimitTestServer(t, defaultPrincipal(), deps)

	for i := 0; i < 20; i++ {
		resp := postCreate(t, srv)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("attempt %d: status=%d, want 201 (limiter disabled)", i+1, resp.StatusCode)
		}
	}
}
