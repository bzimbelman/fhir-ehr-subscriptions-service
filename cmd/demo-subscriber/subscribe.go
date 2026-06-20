// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// defaultHTTPTimeoutNanos holds the per-request timeout the demo CLIs
// apply when the caller does not pass an HTTPClient. Nanoseconds in an
// atomic int64 because parallel tests legitimately mutate the value.
//
// OP #161: never fall back to http.DefaultClient (zero timeout) — a
// stalled bridge or token endpoint would otherwise block the demo
// forever, and copy-pasters would inherit the same hazard.
var defaultHTTPTimeoutNanos atomic.Int64

func init() {
	defaultHTTPTimeoutNanos.Store(int64(30 * time.Second))
}

// httpTimeout returns the current default HTTP timeout, safe for
// concurrent reads while tests overwrite it.
func httpTimeout() time.Duration {
	return time.Duration(defaultHTTPTimeoutNanos.Load())
}

// setHTTPTimeoutForTest sets the default; only the demo-subscriber
// CLI flag and tests should call this.
func setHTTPTimeoutForTest(d time.Duration) {
	defaultHTTPTimeoutNanos.Store(int64(d))
}

// SubscribeConfig parameterizes postSubscription.
type SubscribeConfig struct {
	BridgeBaseURL string
	Token         string
	Topic         string
	Filter        string
	ChannelType   string
	Endpoint      string
	HTTPClient    *http.Client
	// ClientID, when non-empty, is sent as the X-Client-Id header so a
	// bridge running in dev-bypass mode (cfg.Auth.AllowDevBypass=true,
	// no audience configured) attaches a Principal to the request.
	// Production-mode bridges ignore the header — the real auth verifier
	// gates on the Authorization bearer token instead.
	ClientID string
}

// FilterPair is a parsed name=value Subscription filter.
type FilterPair struct {
	Param string
	Value string
}

// parseFilter parses a `name=value` form (`patient=ABC123`) into a
// FilterPair. Empty input returns an empty slice (the demo allows a
// no-filter subscription).
func parseFilter(s string) ([]FilterPair, error) {
	if s == "" {
		return nil, nil
	}
	idx := strings.Index(s, "=")
	if idx <= 0 || idx == len(s)-1 {
		return nil, fmt.Errorf("filter %q must be in name=value form", s)
	}
	return []FilterPair{{Param: s[:idx], Value: s[idx+1:]}}, nil
}

// postSubscription builds a Subscription JSON, POSTs it to the
// bridge's `/Subscription` endpoint, and returns the new subscription
// id parsed out of the Location header (or `id` field as a fallback).
func postSubscription(ctx context.Context, cfg SubscribeConfig) (string, error) {
	filters, err := parseFilter(cfg.Filter)
	if err != nil {
		return "", err
	}
	channelType := cfg.ChannelType
	if channelType == "" {
		channelType = "rest-hook"
	}

	// OP #157: emit a single channel-spec shape.
	// The bridge's Subscription JSON schema (internal/api/schemas/
	// subscription.schema.json) requires the R4B/DSTU `channel`
	// object — that is the production-supported path today. Future
	// schema work to add full R5 support is tracked separately;
	// when that lands, flip the demo CLI to top-level `channelType`
	// + `endpoint`. Until then, picking the legacy block as the
	// single source of truth keeps the demo working AND removes
	// the brittle "both shapes at once" the original bug
	// (inventory line 118) called out.
	body := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"topic":        cfg.Topic,
		"content":      "full-resource",
		"channel": map[string]any{
			"type":     channelType,
			"endpoint": cfg.Endpoint,
		},
	}
	if len(filters) > 0 {
		filterBy := make([]any, 0, len(filters))
		for _, f := range filters {
			filterBy = append(filterBy, map[string]any{
				"filterParameter": f.Param,
				"value":           f.Value,
			})
		}
		body["filterBy"] = filterBy
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.BridgeBaseURL, "/")+"/Subscription", bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	if cfg.ClientID != "" {
		req.Header.Set("X-Client-Id", cfg.ClientID)
	}

	client := cfg.HTTPClient
	if client == nil {
		// OP #161: bounded-timeout default so a stalled bridge does
		// not block the demo forever. http.DefaultClient is zero-
		// timeout and unsafe to copy into operator code.
		client = &http.Client{Timeout: httpTimeout()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("POST /Subscription: status %d body=%s", resp.StatusCode, string(respBody))
	}

	if loc := resp.Header.Get("Location"); loc != "" {
		return strings.TrimPrefix(loc, "/Subscription/"), nil
	}
	var parsed struct {
		ID string `json:"id"`
	}
	if uErr := json.Unmarshal(respBody, &parsed); uErr == nil && parsed.ID != "" {
		return parsed.ID, nil
	}
	return "", errors.New("no Location header and no id field on Subscription create response")
}

// MintConfig parameterizes mintToken.
type MintConfig struct {
	TokenURL   string
	ClientID   string
	Scope      string
	PrivateKey *rsa.PrivateKey
	Kid        string
	HTTPClient *http.Client
	Now        func() time.Time
}

// mintToken signs a SMART Backend Services client_assertion JWT and
// posts it to the bridge's `/token` endpoint. Returns the access_token
// on success.
func mintToken(ctx context.Context, cfg MintConfig) (string, error) {
	if cfg.TokenURL == "" {
		return "", errors.New("mintToken: TokenURL required")
	}
	if cfg.ClientID == "" {
		return "", errors.New("mintToken: ClientID required")
	}
	if cfg.PrivateKey == nil {
		return "", errors.New("mintToken: PrivateKey required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		// OP #161: bounded-timeout default. See note on
		// httpTimeout() — http.DefaultClient is unsafe here.
		httpClient = &http.Client{Timeout: httpTimeout()}
	}

	t := now()
	claims := jwt.MapClaims{
		"iss": cfg.ClientID,
		"sub": cfg.ClientID,
		"aud": cfg.TokenURL,
		"jti": uuid.NewString(),
		"iat": t.Unix(),
		"exp": t.Add(2 * time.Minute).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if cfg.Kid != "" {
		tok.Header["kid"] = cfg.Kid
	}
	signed, err := tok.SignedString(cfg.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("sign assertion: %w", err)
	}

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", signed)
	if cfg.Scope != "" {
		form.Set("scope", cfg.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST /token: status %d body=%s", resp.StatusCode, string(body))
	}
	var parsed struct {
		AccessToken string `json:"access_token"`
	}
	if uErr := json.Unmarshal(body, &parsed); uErr != nil {
		return "", fmt.Errorf("parse /token response: %w", uErr)
	}
	if parsed.AccessToken == "" {
		return "", errors.New("token endpoint returned no access_token")
	}
	return parsed.AccessToken, nil
}
