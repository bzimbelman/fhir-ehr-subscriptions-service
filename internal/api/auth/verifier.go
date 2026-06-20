// Copyright the fhir-ehr-subscriptions-service authors.
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
	"golang.org/x/sync/singleflight"
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

	// Metrics, if non-nil, receives RecordAuthFailure(reason) calls for
	// every verification failure. The token endpoint owns
	// RecordTokenIssued; the verifier never calls it.
	Metrics MetricsRecorder

	// Now returns the current time. Tests substitute. Default time.Now.
	Now func() time.Time

	// AllowInsecureJWKS permits client JWKS URLs whose scheme is
	// `http://`. The default refuses them (B-12). Local-dev only.
	AllowInsecureJWKS bool

	// JWKSAllowedHosts, when non-empty, restricts the hostnames that
	// can be used as a client JWKS URL. The default (empty) permits
	// any host but the deployment is expected to log a warning at
	// startup.
	JWKSAllowedHosts []string

	// JWKSFetchTimeout caps each JWKS HTTP fetch. Default 5s (B-12).
	JWKSFetchTimeout time.Duration
}

// Verifier authenticates SMART Backend Services bearer tokens.
type Verifier struct {
	cfg VerifierConfig

	jwksMu    sync.Mutex
	jwksCache map[string]jwksEntry
	jwksPolicy

	// jwksFetchGroup deduplicates concurrent first-time JWKS fetches
	// per-URL (OP #202). Without it, N concurrent requests for the
	// same uncached jwksURL stamp N HTTP fetches at the IdP — a
	// request-stampede the verifier should not generate.
	jwksFetchGroup singleflight.Group
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
	if cfg.JWKSFetchTimeout <= 0 {
		cfg.JWKSFetchTimeout = DefaultJWKSFetchTimeout
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.JWKSFetchTimeout}
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
		jwksPolicy: jwksPolicy{
			allowInsecure: cfg.AllowInsecureJWKS,
			allowedHosts:  normalizeHosts(cfg.JWKSAllowedHosts),
		},
	}, nil
}

// Authenticate parses, verifies, and resolves the principal off of the
// request's Authorization header. The returned status is 0 on success;
// otherwise the HTTP status to return (401 or 403) and a short reason
// string suitable for OperationOutcome.diagnostics.
func (v *Verifier) Authenticate(r *http.Request) (principal *Principal, status int, reason string) {
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, "Bearer ") {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "missing bearer token"
	}
	tok := strings.TrimPrefix(header, "Bearer ")
	tok = strings.TrimSpace(tok)
	if tok == "" {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "empty bearer token"
	}

	// First, decode without verification to extract the client_id so we
	// can resolve which JWKS to verify against. This is safe because
	// signature verification still has to pass below; the client lookup
	// is just routing.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(tok, jwt.MapClaims{})
	if err != nil {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "malformed token"
	}
	claims, ok := unverified.Claims.(jwt.MapClaims)
	if !ok {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "malformed token"
	}

	clientID, _ := claims["client_id"].(string)
	if clientID == "" {
		clientID, _ = claims["sub"].(string)
	}
	if clientID == "" {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "missing client_id and sub"
	}

	client, err := v.cfg.ClientLookup.GetByID(r.Context(), clientID)
	if err != nil {
		v.recordFailure("unknown_client")
		return nil, http.StatusUnauthorized, "client lookup failed"
	}
	if client == nil {
		v.recordFailure("unknown_client")
		return nil, http.StatusUnauthorized, "unknown client_id"
	}

	// Choose the validation path. Tokens issued by this server are
	// HS256-signed with IssuedSecret; we detect them by iss claim.
	iss, _ := claims["iss"].(string)
	useServerKey := v.cfg.IssuedSecret != nil && v.cfg.IssuedIssuer != "" && iss == v.cfg.IssuedIssuer

	var keyFn jwt.Keyfunc
	var validMethods []string
	switch {
	case useServerKey:
		secret := v.cfg.IssuedSecret
		keyFn = func(_ *jwt.Token) (any, error) { return secret, nil }
		validMethods = []string{"HS256"}
	case client.JwksURL == "":
		v.recordFailure("unknown_client")
		return nil, http.StatusUnauthorized, "client has no jwks_url configured"
	default:
		kf, kfErr := v.keyfuncFor(r.Context(), client.JwksURL)
		if kfErr != nil {
			v.recordFailure("signature")
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
			v.recordFailure("expired")
			return nil, http.StatusUnauthorized, "token expired"
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			v.recordFailure("signature")
			return nil, http.StatusUnauthorized, "signature invalid"
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			v.recordFailure("expired")
			return nil, http.StatusUnauthorized, "token not yet valid"
		case strings.Contains(msg, "kid"):
			v.recordFailure("signature")
			return nil, http.StatusUnauthorized, "unknown signing key"
		default:
			v.recordFailure("signature")
			return nil, http.StatusUnauthorized, "token validation failed"
		}
	}
	if !verified.Valid {
		v.recordFailure("signature")
		return nil, http.StatusUnauthorized, "token invalid"
	}

	verifiedClaims, ok := verified.Claims.(jwt.MapClaims)
	if !ok {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "malformed claims"
	}

	if !audienceMatches(verifiedClaims["aud"], v.cfg.Audience) {
		v.recordFailure("audience")
		return nil, http.StatusUnauthorized, "audience mismatch"
	}

	// RFC 7523 §3 mandates jti; without it, replay detection silently
	// disengages (B-7). Reject tokens with missing/empty jti as
	// malformed.
	jti, _ := verifiedClaims["jti"].(string)
	if jti == "" {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "missing jti"
	}
	exp, err := claimToTime(verifiedClaims["exp"])
	if err != nil {
		v.recordFailure("malformed")
		return nil, http.StatusUnauthorized, "missing exp"
	}

	// Atomic replay-check + record. Separate Seen + Put across two lock
	// acquisitions left a TOCTOU window where two concurrent requests
	// with the same jti could both authenticate (OP #110).
	//
	// Note: this records the jti before the scope check below. If the
	// scope check fails (no authorized scopes → 403), a retry with the
	// same JWT will surface as "token replay" instead of "no authorized
	// scopes". That's acceptable: replaying a JWT is itself a protocol
	// error, and both responses are unauthorized outcomes.
	if v.cfg.JTICache.CheckAndPut(jti, exp) {
		v.recordFailure("replayed_jti")
		return nil, http.StatusUnauthorized, "token replay"
	}

	scope, _ := verifiedClaims["scope"].(string)
	tokenScopes := splitScope(scope)
	effective := intersect(tokenScopes, client.Scopes)
	if len(effective) == 0 {
		v.recordFailure("revoked")
		return nil, http.StatusForbidden, "no authorized scopes"
	}

	return &Principal{
		ClientID: client.ID,
		Scopes:   effective,
		JTI:      jti,
		Exp:      exp,
	}, 0, ""
}

func (v *Verifier) recordFailure(reason string) {
	if v.cfg.Metrics == nil {
		return
	}
	v.cfg.Metrics.RecordAuthFailure(reason)
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
	if err := v.jwksPolicy.validate(jwksURL); err != nil {
		return nil, err
	}
	v.jwksMu.Lock()
	entry, ok := v.jwksCache[jwksURL]
	v.jwksMu.Unlock()
	if ok && v.cfg.Now().Before(entry.expires) {
		return entry.keyfunc, nil
	}

	// OP #202: deduplicate concurrent first-time fetches. singleflight
	// guarantees that for N concurrent Do calls keyed on the same
	// jwksURL exactly one fn body runs; the rest block on its result.
	// We mark this caller as "leader" iff its Do call ran the fn body
	// (the only goroutine that flips leader=true). All other callers
	// joined the in-flight group → record one collision per follower.
	leader := false
	result, err, _ := v.jwksFetchGroup.Do(jwksURL, func() (any, error) {
		leader = true
		// Recheck the cache under the singleflight: a previous group
		// may have populated it in the wall-clock window between this
		// goroutine's pre-Do read above and the singleflight admitting
		// us as leader.
		v.jwksMu.Lock()
		fresh, freshOK := v.jwksCache[jwksURL]
		v.jwksMu.Unlock()
		if freshOK && v.cfg.Now().Before(fresh.expires) {
			return fresh.keyfunc, nil
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, jwksURL, http.NoBody)
		if rerr != nil {
			return nil, fmt.Errorf("auth: build jwks request: %w", rerr)
		}
		resp, rerr := v.cfg.HTTPClient.Do(req)
		if rerr != nil {
			return nil, fmt.Errorf("auth: fetch jwks: %w", rerr)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("auth: jwks status %d", resp.StatusCode)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, MaxJWKSBodyBytes))
		if rerr != nil {
			return nil, fmt.Errorf("auth: read jwks body: %w", rerr)
		}
		kf, rerr := keyfunc.NewJWKSetJSON(body)
		if rerr != nil {
			return nil, fmt.Errorf("auth: parse jwks: %w", rerr)
		}
		v.jwksMu.Lock()
		v.jwksCache[jwksURL] = jwksEntry{
			keyfunc: kf,
			expires: v.cfg.Now().Add(v.cfg.JWKSCacheTTL),
		}
		v.jwksMu.Unlock()
		return kf, nil
	})
	if !leader && v.cfg.Metrics != nil {
		// Followers joined an in-flight fetch instead of issuing their
		// own HTTP request. For N concurrent first-time callers exactly
		// 1 leader runs and N-1 followers record a collision — the
		// stampede-counting semantic the AC names.
		v.cfg.Metrics.RecordJWKSSingleflightCollision()
	}
	if err != nil {
		return nil, err
	}
	kf, ok := result.(keyfunc.Keyfunc)
	if !ok {
		return nil, fmt.Errorf("auth: jwks fetch returned %T; want keyfunc.Keyfunc", result)
	}
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
