//go:build integration

// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Integration tests for the API handlers package. Spins up a real
// Postgres via testcontainers, runs migrations, wires the handlers
// against the real repos and pool, and exercises the full HTTP path
// through chi.
//
// Run with:
//   go test -race -tags integration -timeout 300s ./internal/api/...

package handlers_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/auth"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/handlers"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/migrate"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
)

// integration encapsulates the running test stack.
type integration struct {
	pool     *pgxpool.Pool
	httpSrv  *httptest.Server
	jwksSrv  *httptest.Server
	signer   *integSigner
	clientID string
	now      func() time.Time
}

type integSigner struct {
	priv *rsa.PrivateKey
	kid  string
}

func (s *integSigner) jwks() map[string]any {
	pub := s.priv.PublicKey
	enc := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	return map[string]any{
		"keys": []any{
			map[string]any{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": s.kid,
				"n":   enc(pub.N.Bytes()),
				"e":   enc([]byte{1, 0, 1}),
			},
		},
	}
}

func (s *integSigner) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	out, err := tok.SignedString(s.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return out
}

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	url, err := startPostgresSafe(ctx, t)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	return url
}

// startPostgresSafe wraps tcpostgres.Run with a panic recovery so that
// environments without Docker (where MustExtractDockerHost panics)
// degrade to t.Skip rather than failing the test run.
func startPostgresSafe(ctx context.Context, t *testing.T) (string, error) {
	t.Helper()
	var url string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("docker unavailable: %v", r)
			}
		}()
		container, runErr := tcpostgres.Run(ctx,
			"postgres:16-alpine",
			tcpostgres.WithDatabase("api_test"),
			tcpostgres.WithUsername("test"),
			tcpostgres.WithPassword("test"),
			tcpostgres.BasicWaitStrategies(),
			tcpostgres.WithSQLDriver("pgx/v5"),
		)
		if runErr != nil {
			err = runErr
			return
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		url, err = container.ConnectionString(ctx, "sslmode=disable")
	}()
	return url, err
}

func setupIntegration(t *testing.T) *integration {
	t.Helper()

	url := startPostgres(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := migrate.Up(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	signer := &integSigner{priv: priv, kid: uuid.NewString()}

	mux := http.NewServeMux()
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(signer.jwks())
	})
	jwksSrv := httptest.NewServer(mux)
	t.Cleanup(jwksSrv.Close)

	clientID := "integration-client"
	now := func() time.Time { return time.Now().UTC() }

	authClients := repos.NewAuthClientsRepo()
	if err := authClients.Insert(ctx, pool, repos.AuthClientRow{
		ID:      clientID,
		JwksURL: jwksSrv.URL + "/jwks",
		Scopes: []string{
			"system/Subscription.c",
			"system/Subscription.r",
			"system/Subscription.u",
			"system/Subscription.d",
			"system/Subscription.cruds",
		},
		DisplayName: "integration",
	}); err != nil {
		t.Fatalf("seed client: %v", err)
	}

	topics := repos.NewSubscriptionTopicsRepo()
	if _, err := topics.Insert(ctx, pool, repos.SubscriptionTopicRow{
		URL:     "http://example.org/topics/orders",
		Version: "1.0.0",
		Title:   "Orders",
		Status:  "active",
		Source:  "builtin",
		Body:    []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/topics/orders","status":"active"}`),
	}); err != nil {
		t.Fatalf("seed topic: %v", err)
	}

	verifier, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:     "https://api.example/audience",
		ClientLookup: handlers.NewAuthClientLookup(pool),
		Now:          now,
	})
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	deps := handlers.Deps{
		Subscriptions: handlers.NewPgSubscriptionsStore(pool),
		Topics:        handlers.NewPgTopicsStore(pool),
		Events:        handlers.NewPgEventsStore(pool),
		Deliveries:    handlers.NewPgDeliveriesStore(pool),
		WsTokens:      handlers.NewPgWsBindingTokensStore(pool),
		Audit:         handlers.NewPgAuditStore(pool),
		Channels: handlers.ChannelRegistry{
			"rest-hook": &fakeChannel{resp: handlers.HandshakeSucceeded},
			"websocket": &fakeChannel{resp: handlers.HandshakeSucceeded},
		},
		Now:           now,
		WSBindingTTL:  5 * time.Minute,
		BaseURL:       "https://api.example",
		WSBaseURL:     "wss://api.example/ws",
		ServerVersion: "integration-test",
	}

	r := chi.NewRouter()
	r.Use(verifier.Middleware)
	handlers.RegisterRoutes(r, deps)
	httpSrv := httptest.NewServer(r)
	t.Cleanup(httpSrv.Close)

	return &integration{
		pool:     pool,
		httpSrv:  httpSrv,
		jwksSrv:  jwksSrv,
		signer:   signer,
		clientID: clientID,
		now:      now,
	}
}

func (i *integration) bearer(t *testing.T) string {
	t.Helper()
	now := i.now()
	tok := i.signer.sign(t, jwt.MapClaims{
		"iss":       i.clientID,
		"sub":       i.clientID,
		"aud":       "https://api.example/audience",
		"client_id": i.clientID,
		"jti":       uuid.NewString(),
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
		"scope":     "system/Subscription.cruds system/Subscription.r system/Subscription.c system/Subscription.u system/Subscription.d",
	})
	return "Bearer " + tok
}

func (i *integration) do(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, i.httpSrv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Authorization", i.bearer(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do %s %s: %v", method, path, err)
	}
	return resp
}

func TestIntegration_FullCRUD(t *testing.T) {
	i := setupIntegration(t)

	create := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.org/webhook",
		"content": "id-only",
		"channel": {"type": "rest-hook", "endpoint": "https://example.org/webhook"}
	}`

	resp := i.do(t, http.MethodPost, "/Subscription", create)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	id, _ := got["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id; body=%s", body)
	}

	// Read it back.
	resp = i.do(t, http.MethodGet, "/Subscription/"+id, "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("read status=%d body=%s", resp.StatusCode, body)
	}

	// Search.
	resp = i.do(t, http.MethodGet, "/Subscription", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), id) {
		t.Errorf("search missing newly-created id: %s", body)
	}

	// $status.
	resp = i.do(t, http.MethodGet, "/Subscription/"+id+"/$status", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "SubscriptionStatus") {
		t.Errorf("$status status=%d body=%s", resp.StatusCode, body)
	}

	// Update — change maxCount only.
	update := strings.Replace(create, `"id-only"`, `"id-only","maxCount":3`, 1)
	resp = i.do(t, http.MethodPut, "/Subscription/"+id, update)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d body=%s", resp.StatusCode, body)
	}

	// Delete.
	resp = i.do(t, http.MethodDelete, "/Subscription/"+id, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status=%d", resp.StatusCode)
	}
}

func TestIntegration_AuthRejected(t *testing.T) {
	i := setupIntegration(t)

	// No Authorization header.
	req, _ := http.NewRequest(http.MethodGet, i.httpSrv.URL+"/Subscription", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestIntegration_ExpiredToken(t *testing.T) {
	i := setupIntegration(t)
	now := i.now()
	expired := i.signer.sign(t, jwt.MapClaims{
		"iss":       i.clientID,
		"sub":       i.clientID,
		"aud":       "https://api.example/audience",
		"client_id": i.clientID,
		"jti":       uuid.NewString(),
		"iat":       now.Add(-2 * time.Hour).Unix(),
		"exp":       now.Add(-1 * time.Hour).Unix(),
		"scope":     "system/Subscription.cruds",
	})
	req, _ := http.NewRequest(http.MethodGet, i.httpSrv.URL+"/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+expired)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestIntegration_WrongAudience(t *testing.T) {
	i := setupIntegration(t)
	now := i.now()
	tok := i.signer.sign(t, jwt.MapClaims{
		"iss":       i.clientID,
		"sub":       i.clientID,
		"aud":       "https://other-aud",
		"client_id": i.clientID,
		"jti":       uuid.NewString(),
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	})
	req, _ := http.NewRequest(http.MethodGet, i.httpSrv.URL+"/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestIntegration_UnknownClient(t *testing.T) {
	i := setupIntegration(t)
	now := i.now()
	tok := i.signer.sign(t, jwt.MapClaims{
		"iss":       "nobody",
		"sub":       "nobody",
		"aud":       "https://api.example/audience",
		"client_id": "nobody",
		"jti":       uuid.NewString(),
		"iat":       now.Add(-1 * time.Minute).Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
	})
	req, _ := http.NewRequest(http.MethodGet, i.httpSrv.URL+"/Subscription", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status=%d", resp.StatusCode)
	}
}

func TestIntegration_RevokedClient(t *testing.T) {
	i := setupIntegration(t)
	// Wipe the client's scopes via direct UPDATE — simulating revocation.
	if _, err := i.pool.Exec(context.Background(),
		`UPDATE auth_clients SET scopes = $1 WHERE id = $2`,
		[]string{}, i.clientID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	resp := i.do(t, http.MethodGet, "/Subscription", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status=%d body=%s", resp.StatusCode, body)
	}
}

func TestIntegration_GetWsBindingToken(t *testing.T) {
	i := setupIntegration(t)
	create := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "websocket"},
		"endpoint": "wss://example.org/ws",
		"content": "id-only",
		"channel": {"type": "websocket"}
	}`
	resp := i.do(t, http.MethodPost, "/Subscription", create)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, body)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	id, _ := got["id"].(string)

	resp = i.do(t, http.MethodPost, "/Subscription/"+id+"/$get-ws-binding-token", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ws-binding status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"resourceType":"Parameters"`) {
		t.Errorf("body should contain Parameters: %s", body)
	}
}

func TestIntegration_TokenEndpoint(t *testing.T) {
	i := setupIntegration(t)

	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          "https://api.example/audience",
		TokenURL:          "https://api.example/token",
		AccessTokenSecret: []byte("0123456789012345678901234567890123456789"),
		AccessTokenTTL:    5 * time.Minute,
		ClientLookup:      handlers.NewAuthClientLookup(i.pool),
		Now:               i.now,
	})
	if err != nil {
		t.Fatalf("NewTokenEndpoint: %v", err)
	}
	tokenSrv := httptest.NewServer(te)
	defer tokenSrv.Close()

	now := i.now()
	assertion := i.signer.sign(t, jwt.MapClaims{
		"iss": i.clientID,
		"sub": i.clientID,
		"aud": "https://api.example/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})

	form := strings.NewReader(
		"grant_type=client_credentials&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-bearer&client_assertion=" + assertion +
			"&scope=system%2FSubscription.cruds")
	req, _ := http.NewRequest(http.MethodPost, tokenSrv.URL, form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("token status=%d body=%s", resp.StatusCode, body)
	}
}
