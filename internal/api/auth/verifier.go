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
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// ClientRecord is the registered client view returned by ClientLookup.
// It mirrors the relevant fields from auth_clients.
type ClientRecord struct {
	ID      string
	JwksURL string
	Scopes  []string
}

// ClientLookup is the dependency the verifier uses to resolve a client
// by id. The Subscriptions API wires it to repos.AuthClientsRepo.GetByID.
type ClientLookup interface {
	GetByID(ctx context.Context, id string) (*ClientRecord, error)
}

// VerifierConfig collects every dependency the verifier needs. It is
// constructed once at startup.
type VerifierConfig struct {
	// Audience is the deployment's expected aud claim. A token whose
	// aud does not match is rejected.
	Audience string

	// ClockSkew is the tolerance applied to exp. Default 60s.
	ClockSkew time.Duration

	// ClientLookup resolves client records by client_id. Required.
	ClientLookup ClientLookup

	// JTICache, if non-nil, replays are rejected. Defaults to a fresh
	// 100k-entry cache.
	JTICache *JTIReplayCache

	// JWKSCacheTTL is how long a JWKS document is cached. Default 1h.
	JWKSCacheTTL time.Duration

	// HTTPClient is the http.Client used to fetch JWKS. Defaults to
	// http.DefaultClient.
	HTTPClient *http.Client

	// IssuedSecret is the HS256 key used to verify access tokens this
	// server issued via the token endpoint. When set, tokens whose iss
	// equals IssuedIssuer are validated against this key instead of the
	// client's JWKS. Optional.
	IssuedSecret []byte

	// IssuedIssuer is the iss claim value the token endpoint stamps on
	// access tokens it mints. Required if IssuedSecret is set.
	IssuedIssuer string

	// Now returns the current time. Tests substitute. Default time.Now.
	Now func() time.Time
}

// Verifier authenticates SMART Backend Services bearer tokens.
type Verifier struct {
	cfg VerifierConfig

	jwksMu    sync.Mutex
	jwksCache map[string]jwksEntry
}

type jwksEntry struct {
	keyfunc keyfunc.Keyfunc
	expires time.Time
}

// NewVerifier validates cfg and returns a ready verifier.
func NewVerifier(cfg VerifierConfig) (*Verifier, error) {
	if cfg.Audience == "" {
		return nil, errors.New("auth: VerifierConfig.Audience is required")
	}
	if cfg.ClientLookup == nil {
		return nil, errors.New("auth: VerifierConfig.ClientLookup is required")
	}
	if cfg.ClockSkew == 0 {
		cfg.ClockSkew = 60 * time.Second
	}
	if cfg.JWKSCacheTTL == 0 {
		cfg.JWKSCacheTTL = time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.JTICache == nil {
		cfg.JTICache = NewJTIReplayCache(0, cfg.Now)
	}
	return &Verifier{
		cfg:       cfg,
		jwksCache: make(map[string]jwksEntry),
	}, nil
}

// Authenticate parses, verifies, and resolves the principal off of the
// request's Authorization header. The returned status is 0 on success;
// otherwise the HTTP status to return (401 or 403) and a short reason
// string suitable for OperationOutcome.diagnostics.
func (v *Verifier) Authenticate(r *http.Request) (*Principal, int, string) {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		return nil, http.StatusUnauthorized, "missing bearer token"
	}
	tok := strings.TrimPrefix(header, "Bearer ")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return nil, http.StatusUnauthorized, "empty bearer token"
	}

	// First, decode without verification to extract the client_id so we
	// can resolve which JWKS to verify against. This is safe because
	// signature verification still has to pass below; the client lookup
	// is just routing.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		return nil, http.StatusUnauthorized, "malformed token"
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		return nil, http.StatusUnauthorized, "malformed token"
	}

	clientID, _ := claims["client_id"].(string)
	if clientID == "" {
		clientID, _ = claims["sub"].(string)
	}
	if clientID == "" {
		return nil, http.StatusUnauthorized, "missing client_id and sub"
	}

	client, err := v.cfg.ClientLookup.GetByID(r.Context(), clientID)
	if err != nil {
		return nil, http.StatusUnauthorized, "client lookup failed"
	}
	if client == nil {
		return nil, http.StatusUnauthorized, "unknown client_id"
	}

	// Choose the validation path. Tokens issued by this server are
	// HS256-signed with IssuedSecret; we detect them by iss claim.
	iss, _ := claims["iss"].(string)
	useServerKey := v.cfg.IssuedSecret != nil && v.cfg.IssuedIssuer != "" && iss == v.cfg.IssuedIssuer

	var keyFn jwt.Keyfunc
	var validMethods []string
	if useServerKey {
		secret := v.cfg.IssuedSecret
		keyFn = func(token *jwt.Token) (any, error) { return secret, nil }
		validMethods = []string{"HS256"}
	} else {
		if client.JwksURL == "" {
			return nil, http.StatusUnauthorized, "client has no jwks_url configured"
		}
		kf, err := v.keyfuncFor(r.Context(), client.JwksURL)
		if err != nil {
			return nil, http.StatusUnauthorized, "jwks unavailable"
		}
		keyFn = kf.Keyfunc
		validMethods = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}
	}

	// Now verify the signature against the resolved key, with strict
	// claim validation (aud, exp, iat) using the configured clock.
	verified, err := jwt.NewParser(
		jwt.WithValidMethods(validMethods),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.cfg.ClockSkew),
		jwt.WithTimeFunc(v.cfg.Now),
	).Parse(tok, keyFn)
	if err != nil {
		// Map common cases to specific reasons so OperationOutcome
		// diagnostics are informative without leaking key detail.
		msg := err.Error()
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, http.StatusUnauthorized, "token expired"
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, http.StatusUnauthorized, "signature invalid"
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			return nil, http.StatusUnauthorized, "token not yet valid"
		case strings.Contains(msg, "kid"):
			return nil, http.StatusUnauthorized, "unknown signing key"
		default:
			return nil, http.StatusUnauthorized, "token validation failed"
		}
	}
	if !verified.Valid {
		return nil, http.StatusUnauthorized, "token invalid"
	}

	verifiedClaims, ok := verified.Claims.(jwt.MapClaims)
	if !ok {
		return nil, http.StatusUnauthorized, "malformed claims"
	}

	if !audienceMatches(verifiedClaims["aud"], v.cfg.Audience) {
		return nil, http.StatusUnauthorized, "audience mismatch"
	}

	jti, _ := verifiedClaims["jti"].(string)
	if jti != "" {
		if v.cfg.JTICache.Seen(jti) {
			return nil, http.StatusUnauthorized, "token replay"
		}
	}

	exp, err := claimToTime(verifiedClaims["exp"])
	if err != nil {
		return nil, http.StatusUnauthorized, "missing exp"
	}

	scope, _ := verifiedClaims["scope"].(string)
	tokenScopes := splitScope(scope)
	effective := intersect(tokenScopes, client.Scopes)
	if len(effective) == 0 {
		return nil, http.StatusForbidden, "no authorized scopes"
	}

	if jti != "" {
		v.cfg.JTICache.Put(jti, exp)
	}

	return &Principal{
		ClientID: client.ID,
		Scopes:   effective,
		JTI:      jti,
		Exp:      exp,
	}, 0, ""
}

// Middleware wraps next with bearer-token authentication. On failure it
// writes an OperationOutcome and stops the chain.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, status, reason := v.Authenticate(r)
		if status != 0 {
			writeAuthFailure(w, status, reason)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func (v *Verifier) keyfuncFor(ctx context.Context, jwksURL string) (keyfunc.Keyfunc, error) {
	v.jwksMu.Lock()
	entry, ok := v.jwksCache[jwksURL]
	v.jwksMu.Unlock()
	if ok && v.cfg.Now().Before(entry.expires) {
		return entry.keyfunc, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("auth: build jwks request: %w", err)
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read jwks body: %w", err)
	}

	kf, err := keyfunc.NewJWKSetJSON(body)
	if err != nil {
		return nil, fmt.Errorf("auth: parse jwks: %w", err)
	}

	v.jwksMu.Lock()
	v.jwksCache[jwksURL] = jwksEntry{
		keyfunc: kf,
		expires: v.cfg.Now().Add(v.cfg.JWKSCacheTTL),
	}
	v.jwksMu.Unlock()
	return kf, nil
}

func audienceMatches(claim any, expected string) bool {
	switch v := claim.(type) {
	case string:
		return v == expected
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == expected {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if item == expected {
				return true
			}
		}
	}
	return false
}

func splitScope(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func intersect(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if _, ok := set[s]; ok {
			out = append(out, s)
		}
	}
	return out
}

func claimToTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case float64:
		return time.Unix(int64(t), 0), nil
	case int64:
		return time.Unix(t, 0), nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return time.Time{}, err
		}
		return time.Unix(n, 0), nil
	}
	return time.Time{}, errors.New("auth: invalid exp")
}
