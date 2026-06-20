// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPostSubscription_BuildsRestHookBody asserts that PostSubscription
// builds a Subscription resource with the topic, filter, channelType,
// rest-hook endpoint, and a Bearer token header.
func TestPostSubscription_BuildsRestHookBody(t *testing.T) {
	t.Parallel()

	var captured struct {
		auth string
		body []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Subscription" && r.URL.Path != "/Subscription/" {
			t.Errorf("path = %q; want /Subscription[/]", r.URL.Path)
		}
		captured.auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		captured.body = body
		w.Header().Set("Location", "/Subscription/sub-id-1")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"resourceType":"Subscription","id":"sub-id-1"}`))
	}))
	defer srv.Close()

	cfg := SubscribeConfig{
		BridgeBaseURL: srv.URL,
		Token:         "test-token",
		Topic:         "http://demo.org/topics/lab-results",
		Filter:        "patient=ABC123",
		ChannelType:   "rest-hook",
		Endpoint:      "http://127.0.0.1:9000/hook/sub-1",
		HTTPClient:    srv.Client(),
	}
	id, err := postSubscription(context.Background(), cfg)
	if err != nil {
		t.Fatalf("postSubscription: %v", err)
	}
	if id != "sub-id-1" {
		t.Errorf("subscription id: got %q want sub-id-1", id)
	}
	if captured.auth != "Bearer test-token" {
		t.Errorf("Authorization: got %q want %q", captured.auth, "Bearer test-token")
	}

	var sub map[string]any
	if err := json.Unmarshal(captured.body, &sub); err != nil {
		t.Fatalf("body not JSON: %v body=%s", err, captured.body)
	}
	if sub["resourceType"] != "Subscription" {
		t.Errorf("resourceType: got %v want Subscription", sub["resourceType"])
	}
	if sub["topic"] != "http://demo.org/topics/lab-results" {
		t.Errorf("topic: got %v", sub["topic"])
	}
	// OP #157: single channel-spec shape — the bridge schema
	// requires the R4B `channel` block, so that is the only shape
	// the body emits. Top-level R5 `channelType`/`endpoint` are
	// absent (the duplicate state was the hazard the OP called out).
	channel, ok := sub["channel"].(map[string]any)
	if !ok {
		t.Fatalf("channel missing or wrong type: %v", sub["channel"])
	}
	if channel["type"] != "rest-hook" {
		t.Errorf("channel.type: got %v want rest-hook", channel["type"])
	}
	if channel["endpoint"] != "http://127.0.0.1:9000/hook/sub-1" {
		t.Errorf("channel.endpoint: got %v", channel["endpoint"])
	}
	if _, present := sub["channelType"]; present {
		t.Errorf("OP #157: top-level channelType must not duplicate the `channel` block; got %v", sub["channelType"])
	}
	if _, present := sub["endpoint"]; present {
		t.Errorf("OP #157: top-level endpoint must not duplicate the `channel` block; got %v", sub["endpoint"])
	}
	// Filter is preserved in filterBy[].value with name=patient.
	filterBy, ok := sub["filterBy"].([]any)
	if !ok || len(filterBy) != 1 {
		t.Fatalf("filterBy missing or wrong shape: %v", sub["filterBy"])
	}
	fb := filterBy[0].(map[string]any)
	if fb["filterParameter"] != "patient" {
		t.Errorf("filterBy[0].filterParameter: got %v want patient", fb["filterParameter"])
	}
	if fb["value"] != "ABC123" {
		t.Errorf("filterBy[0].value: got %v want ABC123", fb["value"])
	}
}

// TestPostSubscription_NonCreatedReturnsError surfaces the bridge's
// error response so an operator sees the OperationOutcome instead of a
// silent failure.
func TestPostSubscription_NonCreatedReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"diagnostics":"topic not in catalog"}]}`))
	}))
	defer srv.Close()

	cfg := SubscribeConfig{
		BridgeBaseURL: srv.URL,
		Token:         "tok",
		Topic:         "http://demo.org/topics/no-such",
		ChannelType:   "rest-hook",
		Endpoint:      "http://127.0.0.1:1/hook",
		HTTPClient:    srv.Client(),
	}
	_, err := postSubscription(context.Background(), cfg)
	if err == nil {
		t.Fatal("postSubscription: want error; got nil")
	}
	if !strings.Contains(err.Error(), "422") {
		t.Errorf("error missing status code: %v", err)
	}
	if !strings.Contains(err.Error(), "topic not in catalog") {
		t.Errorf("error missing OperationOutcome diagnostic: %v", err)
	}
}

// TestParseFilter_PatientShortForm parses the "patient=ABC123" CLI form
// into a single SubscriptionFilterBy entry.
func TestParseFilter_PatientShortForm(t *testing.T) {
	t.Parallel()
	got, err := parseFilter("patient=ABC123")
	if err != nil {
		t.Fatalf("parseFilter: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d filters; want 1", len(got))
	}
	if got[0].Param != "patient" || got[0].Value != "ABC123" {
		t.Errorf("filter[0]: got %+v", got[0])
	}
}

// TestParseFilter_Empty returns an empty slice (not an error) so the
// operator can subscribe to all events on a topic.
func TestParseFilter_Empty(t *testing.T) {
	t.Parallel()
	got, err := parseFilter("")
	if err != nil || len(got) != 0 {
		t.Fatalf("parseFilter(\"\") = (%v, %v); want ([], nil)", got, err)
	}
}

// TestMintToken_PostsClientAssertion stands up a fake /token endpoint
// and verifies MintToken hits it with grant_type, the right
// client_assertion_type, a JWT containing the configured client_id,
// and that the returned access_token is parsed out of the response.
func TestMintToken_PostsClientAssertion(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}

	var captured struct {
		gotForm bool
		form    map[string]string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q; want POST", r.Method)
		}
		if pErr := r.ParseForm(); pErr != nil {
			t.Errorf("parse form: %v", pErr)
		}
		captured.gotForm = true
		captured.form = map[string]string{
			"grant_type":            r.PostForm.Get("grant_type"),
			"client_assertion_type": r.PostForm.Get("client_assertion_type"),
			"client_assertion":      r.PostForm.Get("client_assertion"),
			"scope":                 r.PostForm.Get("scope"),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "minted-access-token",
			"token_type":   "Bearer",
			"expires_in":   300,
		})
	}))
	defer srv.Close()

	tok, err := mintToken(context.Background(), MintConfig{
		TokenURL:   srv.URL,
		ClientID:   "demo-client",
		Scope:      "system/Subscription.cruds",
		PrivateKey: priv,
		Kid:        "demo-key-1",
		HTTPClient: srv.Client(),
		Now:        func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("mintToken: %v", err)
	}
	if tok != "minted-access-token" {
		t.Errorf("access token: got %q want minted-access-token", tok)
	}
	if !captured.gotForm {
		t.Fatal("token endpoint never received a form-encoded body")
	}
	if got := captured.form["grant_type"]; got != "client_credentials" {
		t.Errorf("grant_type: got %q want client_credentials", got)
	}
	if got := captured.form["client_assertion_type"]; got != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		t.Errorf("client_assertion_type: got %q", got)
	}
	if captured.form["client_assertion"] == "" {
		t.Error("client_assertion empty")
	}
	if got := captured.form["scope"]; got != "system/Subscription.cruds" {
		t.Errorf("scope: got %q", got)
	}
	// Spot-check that the assertion parses as a JWT with the expected
	// header alg + kid; the bridge verifies the signature, the demo
	// just needs to produce a JWT.
	parts := strings.Split(captured.form["client_assertion"], ".")
	if len(parts) != 3 {
		t.Fatalf("assertion not a JWT: %q", captured.form["client_assertion"])
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header map[string]any
	if uErr := json.Unmarshal(headerJSON, &header); uErr != nil {
		t.Fatalf("unmarshal header: %v body=%s", uErr, headerJSON)
	}
	if header["alg"] != "RS256" {
		t.Errorf("alg: got %v want RS256", header["alg"])
	}
	if header["kid"] != "demo-key-1" {
		t.Errorf("kid: got %v want demo-key-1", header["kid"])
	}
	// Sanity: claims contain the right iss/sub/aud.
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]any
	if uErr := json.Unmarshal(claimsJSON, &claims); uErr != nil {
		t.Fatalf("unmarshal claims: %v", uErr)
	}
	if claims["iss"] != "demo-client" {
		t.Errorf("iss: got %v want demo-client", claims["iss"])
	}
	if claims["sub"] != "demo-client" {
		t.Errorf("sub: got %v want demo-client", claims["sub"])
	}
	if claims["aud"] != srv.URL {
		t.Errorf("aud: got %v want %s", claims["aud"], srv.URL)
	}
	if _, ok := claims["jti"].(string); !ok {
		t.Error("jti claim missing or not a string")
	}
}

// TestMintToken_NonOKResponseReturnsError surfaces a 401 from the
// token endpoint so the operator sees the bridge's OperationOutcome.
func TestMintToken_NonOKResponseReturnsError(t *testing.T) {
	t.Parallel()

	priv, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // test-only key
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"diagnostics":"unknown client"}]}`))
	}))
	defer srv.Close()

	_, err = mintToken(context.Background(), MintConfig{
		TokenURL:   srv.URL,
		ClientID:   "demo-client",
		PrivateKey: priv,
		Kid:        "k",
		HTTPClient: srv.Client(),
	})
	if err == nil {
		t.Fatal("mintToken: want error; got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error missing status: %v", err)
	}
}

// rsa.GenerateKey is expensive; ensure tests don't share state in a
// way that would amplify cost. (no-op assertion)
var _ = big.NewInt
