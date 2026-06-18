// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TokenEndpointConfig configures the OAuth2 token endpoint.
type TokenEndpointConfig struct {
	// Audience is the aud value stamped on issued access tokens. Same
	// as the Verifier's expected audience.
	Audience string

	// TokenURL is the canonical URL of this token endpoint. The
	// client_assertion's aud claim must equal this value.
	TokenURL string

	// AccessTokenSecret is the HS256 key used to sign issued access
	// tokens. Must be at least 32 bytes for HS256 security.
	AccessTokenSecret []byte

	// AccessTokenTTL is how long an issued access token is valid.
	// Default 5 minutes per SMART Backend Services guidance.
	AccessTokenTTL time.Duration

	// AccessTokenIssuer is the iss claim on issued tokens. Defaults to
	// Audience.
	AccessTokenIssuer string

	// ClientLookup resolves clients by client_id. Same dependency as
	// the Verifier.
	ClientLookup ClientLookup

	// HTTPClient is used to fetch client JWKS docs. Default
	// http.DefaultClient.
	HTTPClient *http.Client

	// JWKSCacheTTL is how long a JWKS document is cached. Default 1h.
	JWKSCacheTTL time.Duration

	// ClockSkew is the leeway applied to the assertion's exp/iat.
	// Default 60s.
	ClockSkew time.Duration

	// Now returns the current time. Tests substitute.
	Now func() time.Time
}

// TokenEndpoint implements the SMART on FHIR Backend Services token
// endpoint. It accepts a JWT bearer client assertion and returns an
// access token signed with AccessTokenSecret.
type TokenEndpoint struct {
	cfg       TokenEndpointConfig
	jwksCache *jwksCache
}

// NewTokenEndpoint validates cfg and returns a ready handler.
func NewTokenEndpoint(cfg TokenEndpointConfig) (*TokenEndpoint, error) {
	if cfg.Audience == "" {
		return nil, errors.New("auth: TokenEndpointConfig.Audience required")
	}
	if cfg.TokenURL == "" {
		return nil, errors.New("auth: TokenEndpointConfig.TokenURL required")
	}
	if len(cfg.AccessTokenSecret) == 0 {
		return nil, errors.New("auth: TokenEndpointConfig.AccessTokenSecret required")
	}
	if cfg.ClientLookup == nil {
		return nil, errors.New("auth: TokenEndpointConfig.ClientLookup required")
	}
	if cfg.AccessTokenTTL == 0 {
		cfg.AccessTokenTTL = 5 * time.Minute
	}
	if cfg.AccessTokenIssuer == "" {
		cfg.AccessTokenIssuer = cfg.Audience
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.JWKSCacheTTL == 0 {
		cfg.JWKSCacheTTL = time.Hour
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 60 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &TokenEndpoint{
		cfg:       cfg,
		jwksCache: newJwksCache(cfg.HTTPClient, cfg.JWKSCacheTTL, cfg.Now),
	}, nil
}

// ServeHTTP handles POST /token per RFC 6749 with the SMART Backend
// Services profile.
func (te *TokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "could not parse form")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"only client_credentials is supported")
		return
	}
	if r.PostForm.Get("client_assertion_type") !=
		"urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client",
			"client_assertion_type must be jwt-bearer")
		return
	}
	assertion := r.PostForm.Get("client_assertion")
	if assertion == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client",
			"client_assertion required")
		return
	}

	// Decode unverified to discover the client_id.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"malformed assertion")
		return
	}
	claims, _ := unverified.Claims.(jwt.MapClaims)

	clientID, _ := claims["client_id"].(string)
	if clientID == "" {
		clientID, _ = claims["sub"].(string)
	}
	if clientID == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"missing client_id/sub")
		return
	}

	client, err := te.cfg.ClientLookup.GetByID(r.Context(), clientID)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error",
			"client lookup failed")
		return
	}
	if client == nil || client.JwksURL == "" {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"unknown client")
		return
	}

	kf, err := te.jwksCache.fetch(r.Context(), client.JwksURL)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"jwks unavailable")
		return
	}

	// Verify the assertion's signature with strict claim checks. The
	// audience MUST equal the token URL per RFC 7523.
	verified, err := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(te.cfg.ClockSkew),
		jwt.WithTimeFunc(te.cfg.Now),
	).Parse(assertion, kf.Keyfunc)
	if err != nil {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			fmt.Sprintf("assertion validation failed: %v", err))
		return
	}
	if !verified.Valid {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"assertion invalid")
		return
	}
	vc, _ := verified.Claims.(jwt.MapClaims)

	// iss/sub MUST equal client_id; aud MUST be the token URL.
	if iss, _ := vc["iss"].(string); iss != clientID {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "iss mismatch")
		return
	}
	if sub, _ := vc["sub"].(string); sub != clientID {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "sub mismatch")
		return
	}
	if !audienceMatches(vc["aud"], te.cfg.TokenURL) {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client",
			"assertion aud must equal token URL")
		return
	}

	// Compute granted scopes — intersect requested with client.Scopes.
	requested := splitScope(r.PostForm.Get("scope"))
	if len(requested) == 0 {
		requested = client.Scopes
	}
	granted := intersect(requested, client.Scopes)
	if len(granted) == 0 {
		writeOAuthError(w, http.StatusForbidden, "invalid_scope",
			"no authorized scopes")
		return
	}

	now := te.cfg.Now()
	exp := now.Add(te.cfg.AccessTokenTTL)
	access := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":       te.cfg.AccessTokenIssuer,
		"sub":       client.ID,
		"client_id": client.ID,
		"aud":       te.cfg.Audience,
		"jti":       uuid.NewString(),
		"iat":       now.Unix(),
		"exp":       exp.Unix(),
		"scope":     strings.Join(granted, " "),
	})
	signed, err := access.SignedString(te.cfg.AccessTokenSecret)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error",
			"signing failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": signed,
		"token_type":   "Bearer",
		"expires_in":   int(te.cfg.AccessTokenTTL.Seconds()),
		"scope":        strings.Join(granted, " "),
	})
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error":             code,
		"error_description": desc,
	})
}

// jwksCache is a tiny TTL cache of compiled keyfuncs keyed by URL.
// Sharing a cache between Verifier and TokenEndpoint avoids
// re-fetching JWKS for every request.
type jwksCache struct {
	httpClient *http.Client
	ttl        time.Duration
	now        func() time.Time
	entries    map[string]jwksCacheEntry
}

type jwksCacheEntry struct {
	kf      keyfunc.Keyfunc
	expires time.Time
}

func newJwksCache(httpClient *http.Client, ttl time.Duration, now func() time.Time) *jwksCache {
	return &jwksCache{
		httpClient: httpClient,
		ttl:        ttl,
		now:        now,
		entries:    map[string]jwksCacheEntry{},
	}
}

func (c *jwksCache) fetch(ctx context.Context, url string) (keyfunc.Keyfunc, error) {
	if e, ok := c.entries[url]; ok && c.now().Before(e.expires) {
		return e.kf, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	kf, err := keyfunc.NewJWKSetJSON(body)
	if err != nil {
		return nil, err
	}
	c.entries[url] = jwksCacheEntry{kf: kf, expires: c.now().Add(c.ttl)}
	return kf, nil
}
