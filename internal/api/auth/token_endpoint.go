// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// MaxTokenRequestBodyBytes is the default ceiling on the size of a
// /token request body, applied via http.MaxBytesReader before
// ParseForm. The endpoint is unauthenticated; without a body cap an
// attacker can OOM the process with a single multi-MB POST.
const MaxTokenRequestBodyBytes = 64 * 1024

// MaxJWKSBodyBytes caps each JWKS HTTP response. Operators are
// expected to host JWKS docs an order of magnitude smaller; 1 MiB is
// a generous ceiling that prevents a hostile or misconfigured JWKS
// host from exhausting auth memory (B-12).
const MaxJWKSBodyBytes = 1024 * 1024

// DefaultJWKSFetchTimeout is the per-fetch timeout applied when no
// override is supplied via TokenEndpointConfig.JWKSFetchTimeout (B-12).
const DefaultJWKSFetchTimeout = 5 * time.Second

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

	// Metrics, if non-nil, receives auth-failure and token-issued
	// counters. The token endpoint treats every assertion failure as an
	// auth failure with the reason mapped to the canonical set
	// (malformed, signature, expired, audience, unknown_client).
	Metrics MetricsRecorder

	// Now returns the current time. Tests substitute.
	Now func() time.Time

	// MaxRequestBodyBytes caps the request body size accepted by
	// ServeHTTP. Default MaxTokenRequestBodyBytes (64 KiB).
	MaxRequestBodyBytes int64

	// Logger receives detailed assertion-failure errors so operators
	// can debug client misbehaviour. The HTTP response body never
	// contains the underlying jwt library error string. Optional; if
	// nil, detailed errors are dropped.
	Logger *slog.Logger

	// AllowInsecureJWKS permits client JWKS URLs whose scheme is
	// `http://`. The default refuses them (B-12). Local-dev only.
	AllowInsecureJWKS bool

	// JWKSAllowedHosts, when non-empty, restricts the hostnames that
	// can be used as a client JWKS URL. The default (empty) permits
	// any host but logs a warning at startup. Hostnames are compared
	// case-insensitively (no port).
	JWKSAllowedHosts []string

	// JWKSFetchTimeout caps each JWKS HTTP fetch. Default 5s (B-12).
	JWKSFetchTimeout time.Duration

	// RateLimitPerSource configures a per-source-IP token bucket. The
	// /token endpoint is unauthenticated and CPU-intensive (RSA verify
	// on user-controlled bytes); without an upstream rate limit, an
	// attacker can drive RSA-verify CPU exhaustion at line rate (S-3).
	// Zero (default) disables limiting; production deployments SHOULD
	// configure a non-zero value or front the endpoint with a rate-
	// limiting reverse proxy.
	RateLimitPerSource RateLimit
}

// TokenEndpoint implements the SMART on FHIR Backend Services token
// endpoint. It accepts a JWT bearer client assertion and returns an
// access token signed with AccessTokenSecret.
type TokenEndpoint struct {
	cfg       TokenEndpointConfig
	jwksCache *jwksCache
	jtiCache  *JTIReplayCache
	limiter   *rateLimiter
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
	if cfg.JWKSFetchTimeout <= 0 {
		cfg.JWKSFetchTimeout = DefaultJWKSFetchTimeout
	}
	if cfg.HTTPClient == nil {
		// Dedicated client with timeout — http.DefaultClient has none,
		// which lets a slow/hostile JWKS host hang every authentication
		// call indefinitely (B-12).
		cfg.HTTPClient = &http.Client{Timeout: cfg.JWKSFetchTimeout}
	}
	if cfg.JWKSCacheTTL == 0 {
		cfg.JWKSCacheTTL = time.Hour
	}
	if cfg.ClockSkew == 0 {
		// Default to 30s per SMART Backend Services / RFC 7523 guidance:
		// 60s is generous and widens the JTI replay window. Deployments
		// can override via config (S-3).
		cfg.ClockSkew = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxRequestBodyBytes <= 0 {
		cfg.MaxRequestBodyBytes = MaxTokenRequestBodyBytes
	}
	policy := jwksPolicy{
		allowInsecure: cfg.AllowInsecureJWKS,
		allowedHosts:  normalizeHosts(cfg.JWKSAllowedHosts),
	}
	te := &TokenEndpoint{
		cfg:       cfg,
		jwksCache: newJwksCache(cfg.HTTPClient, cfg.JWKSCacheTTL, cfg.Now, policy),
		jtiCache:  NewJTIReplayCache(0, cfg.Now),
	}
	if cfg.RateLimitPerSource.Burst > 0 {
		te.limiter = newRateLimiter(cfg.RateLimitPerSource, cfg.Now)
	}
	return te, nil
}

// Close releases the token endpoint's HTTP transport idle connections.
// The JWKS fetcher is a long-lived http.Client that holds keep-alive
// sockets to the operator's IDP; on graceful shutdown those connections
// must be released so the process exits without warm sockets (OP #208).
// Idempotent: a caller-supplied client whose Transport is not a
// *http.Transport (e.g. a wrapping middleware) is left untouched.
func (te *TokenEndpoint) Close() error {
	if te == nil || te.cfg.HTTPClient == nil {
		return nil
	}
	if tr, ok := te.cfg.HTTPClient.Transport.(*http.Transport); ok && tr != nil {
		tr.CloseIdleConnections()
	}
	return nil
}

// ServeHTTP handles POST /token per RFC 6749 with the SMART Backend
// Services profile.
func (te *TokenEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		te.fail(w, http.StatusMethodNotAllowed, "invalid_request", "method_not_allowed")
		return
	}
	// Per-source rate limit before any expensive work. The endpoint is
	// unauthenticated and JWT verification is RSA-CPU-bound (S-3).
	if te.limiter != nil {
		key := rateLimitKey(r)
		if ok, retryAfter := te.limiter.Allow(key); !ok {
			if retryAfter > 0 {
				secs := int(retryAfter.Seconds())
				if secs < 1 {
					secs = 1
				}
				w.Header().Set("Retry-After", strconv.Itoa(secs))
			}
			te.fail(w, http.StatusTooManyRequests, "slow_down", "rate_limited")
			return
		}
	}
	// Cap the request body before ParseForm consumes it. The endpoint
	// is unauthenticated, so without this an attacker can flood
	// arbitrary-sized POSTs to exhaust memory (B-6).
	r.Body = http.MaxBytesReader(w, r.Body, te.cfg.MaxRequestBodyBytes)
	if err := r.ParseForm(); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			te.fail(w, http.StatusRequestEntityTooLarge, "invalid_request", "body_too_large")
			return
		}
		te.fail(w, http.StatusBadRequest, "invalid_request", "parse_form_failed")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		te.fail(w, http.StatusBadRequest, "unsupported_grant_type", "unsupported_grant_type")
		return
	}
	if r.PostForm.Get("client_assertion_type") !=
		"urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		te.fail(w, http.StatusBadRequest, "invalid_client", "assertion_type_mismatch")
		return
	}
	assertion := r.PostForm.Get("client_assertion")
	if assertion == "" {
		te.fail(w, http.StatusBadRequest, "invalid_client", "assertion_required")
		return
	}

	// Decode unverified to discover the client_id.
	parser := jwt.NewParser(jwt.WithoutClaimsValidation())
	unverified, _, err := parser.ParseUnverified(assertion, jwt.MapClaims{})
	if err != nil {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "malformed")
		return
	}
	claims, _ := unverified.Claims.(jwt.MapClaims)

	clientID, _ := claims["client_id"].(string)
	if clientID == "" {
		clientID, _ = claims["sub"].(string)
	}
	if clientID == "" {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "missing_client_id")
		return
	}

	client, err := te.cfg.ClientLookup.GetByID(r.Context(), clientID)
	if err != nil {
		te.fail(w, http.StatusInternalServerError, "server_error", "client_lookup_failed")
		return
	}
	if client == nil || client.JwksURL == "" {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "unknown_client")
		return
	}

	kf, err := te.jwksCache.fetch(r.Context(), client.JwksURL)
	if err != nil {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "signature")
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
		// B-8: golang-jwt error strings include "crypto/rsa:
		// verification error", key ids, algorithm names. Map to a
		// fixed enum of generic strings; log the detail server-side.
		reason := classifyAssertionErr(err)
		te.logAssertionFailure(r, clientID, reason, err)
		te.fail(w, http.StatusUnauthorized, "invalid_client", reason)
		return
	}
	if !verified.Valid {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "signature")
		return
	}
	vc, _ := verified.Claims.(jwt.MapClaims)

	// Required claims per RFC 7523.
	if _, ok := vc["exp"]; !ok {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "missing_exp")
		return
	}
	if _, ok := vc["iat"]; !ok {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "missing_iat")
		return
	}
	if iss, _ := vc["iss"].(string); iss != clientID {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "iss_mismatch")
		return
	}
	if sub, _ := vc["sub"].(string); sub != clientID {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "sub_mismatch")
		return
	}
	if !audienceMatches(vc["aud"], te.cfg.TokenURL) {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "audience")
		return
	}

	// RFC 7523 §3 mandates jti; without it, replay detection is
	// silently bypassed (B-7). Treat missing/empty jti as malformed.
	jti, _ := vc["jti"].(string)
	if jti == "" {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "missing_jti")
		return
	}
	assertionExp, err := claimToTime(vc["exp"])
	if err != nil {
		// S-3: a swallowed parse error here causes Put(jti, time.Time{}),
		// which silently disables replay protection for the issued token.
		// Fail closed so JTI tracking always carries a real expiry.
		te.fail(w, http.StatusUnauthorized, "invalid_client", "exp_invalid")
		return
	}
	// Atomic replay-check + record. Separate Seen + Put across two lock
	// acquisitions left a TOCTOU window where two concurrent /token
	// POSTs with the same jti could both succeed (OP #110).
	if te.jtiCache.CheckAndPut(jti, assertionExp) {
		te.fail(w, http.StatusUnauthorized, "invalid_client", "replayed_jti")
		return
	}

	// Compute granted scopes — intersect requested with client.Scopes.
	requested := splitScope(r.PostForm.Get("scope"))
	if len(requested) == 0 {
		requested = client.Scopes
	}
	granted := intersect(requested, client.Scopes)
	if len(granted) == 0 {
		te.fail(w, http.StatusForbidden, "invalid_scope", "revoked")
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
		te.fail(w, http.StatusInternalServerError, "server_error", "server_error")
		return
	}

	if te.cfg.Metrics != nil {
		te.cfg.Metrics.RecordTokenIssued()
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

// fail writes an OAuth-formatted OperationOutcome and records the
// matching auth_failures metric. The diagnostic on the wire is derived
// from the typed reason via diagnosticForReason so every callsite emits
// a stable enum-mapped string and operators see consistent reason codes
// in logs and metric labels (OP #221).
func (te *TokenEndpoint) fail(w http.ResponseWriter, status int, code, reason string) {
	if te.cfg.Metrics != nil {
		te.cfg.Metrics.RecordAuthFailure(reason)
	}
	writeOAuthError(w, status, code, diagnosticForReason(reason))
}

func classifyAssertionErr(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenExpired):
		return "expired"
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		return "signature"
	case errors.Is(err, jwt.ErrTokenNotValidYet):
		return "expired"
	case strings.Contains(err.Error(), "kid"):
		return "signature"
	default:
		return "signature"
	}
}

// diagnosticForReason returns a fixed, generic diagnostic string for
// each canonical auth-failure reason. The message MUST NOT include any
// caller-controlled data, library-internal phrases, or key material;
// see B-8.
//
// OP #221: every te.fail callsite flows its diagnostic through this
// function. Reasons cover the three /token endpoint stages — wire
// (method, body, form), grant validation (grant_type, assertion shape),
// and assertion claim checks (exp, iat, iss/sub, aud, jti, scope).
func diagnosticForReason(reason string) string {
	switch reason {
	// Assertion validation outcomes (B-8 enum).
	case "expired":
		return "assertion expired"
	case "audience":
		return "assertion audience mismatch"
	case "malformed":
		return "assertion malformed"
	case "unknown_client":
		return "unknown client"
	case "replayed_jti":
		return "assertion jti replay"

	// Wire-level rejections (OP #221).
	case "method_not_allowed":
		return "method not allowed"
	case "body_too_large":
		return "request body too large"
	case "parse_form_failed":
		return "could not parse form"
	case "unsupported_grant_type":
		return "only client_credentials is supported"
	case "rate_limited":
		return "rate limit exceeded"
	case "server_error":
		return "server error"

	// Granular assertion-shape rejections — each preserves a stable,
	// caller-agnostic message routed through this enum.
	case "assertion_type_mismatch":
		return "client_assertion_type must be jwt-bearer"
	case "assertion_required":
		return "client_assertion required"
	case "missing_client_id":
		return "missing client_id/sub"
	case "client_lookup_failed":
		return "client lookup failed"
	case "missing_exp":
		return "missing exp"
	case "missing_iat":
		return "missing iat"
	case "iss_mismatch":
		return "iss mismatch"
	case "sub_mismatch":
		return "sub mismatch"
	case "missing_jti":
		return "missing jti"
	case "exp_invalid":
		return "assertion exp invalid"
	case "revoked":
		return "no authorized scopes"

	// Default catches "signature" and any unrecognized future reasons.
	default:
		return "assertion invalid"
	}
}

// logAssertionFailure emits a single structured log line with the raw
// jwt error so operators can debug; never reaches the wire.
func (te *TokenEndpoint) logAssertionFailure(r *http.Request, clientID, reason string, err error) {
	if te.cfg.Logger == nil {
		return
	}
	te.cfg.Logger.LogAttrs(r.Context(), slog.LevelInfo, "auth: assertion validation failed",
		slog.String("client_id", clientID),
		slog.String("reason", reason),
		slog.String("error", err.Error()),
	)
}

// writeOAuthError emits the RFC 6749 OAuth error code wrapped in a FHIR
// OperationOutcome. LLD §10 requires every error response from the API
// — including the token endpoint — to be an OperationOutcome. The OAuth
// error code is preserved on the issue's details.coding so spec-aware
// clients can still pull it out, and the `error_description` text is
// surfaced verbatim as `diagnostics`.
//
// HTTP status codes follow RFC 6749 unchanged.
func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/fhir+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	body := map[string]any{
		"resourceType": "OperationOutcome",
		"issue": []any{
			map[string]any{
				"severity":    "error",
				"code":        "security",
				"diagnostics": desc,
				"details": map[string]any{
					"coding": []any{
						map[string]any{
							"system":  "urn:ietf:rfc:6749",
							"code":    code,
							"display": desc,
						},
					},
				},
			},
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}

// jwksCache is a tiny TTL cache of compiled keyfuncs keyed by URL.
// Sharing a cache between Verifier and TokenEndpoint avoids
// re-fetching JWKS for every request.
//
// All access to entries is gated by mu. Concurrent /token requests
// otherwise race the map and Go's runtime fatal-errors the process
// (B-5). The cache also enforces the JWKS-URL policy from
// TokenEndpointConfig (B-12): default-https, optional host allowlist,
// 1 MiB body cap.
type jwksCache struct {
	httpClient *http.Client
	ttl        time.Duration
	now        func() time.Time
	policy     jwksPolicy

	mu      sync.Mutex
	entries map[string]jwksCacheEntry
}

type jwksCacheEntry struct {
	kf      keyfunc.Keyfunc
	expires time.Time
}

// jwksPolicy captures the validation rules applied to a candidate
// JWKS URL before any network I/O. Empty allowedHosts means
// "any host" (with the deployment expected to surface a startup
// warning).
type jwksPolicy struct {
	allowInsecure bool
	allowedHosts  map[string]struct{}
}

func newJwksCache(httpClient *http.Client, ttl time.Duration, now func() time.Time, policy jwksPolicy) *jwksCache {
	return &jwksCache{
		httpClient: httpClient,
		ttl:        ttl,
		now:        now,
		policy:     policy,
		entries:    map[string]jwksCacheEntry{},
	}
}

// errInvalidJWKSURL is returned when the JWKS URL fails policy checks
// (scheme, host allowlist). Callers map this to a generic 401.
var errInvalidJWKSURL = errors.New("auth: invalid jwks_url")

func (c *jwksCache) fetch(ctx context.Context, rawURL string) (keyfunc.Keyfunc, error) {
	if err := c.policy.validate(rawURL); err != nil {
		return nil, err
	}
	c.mu.Lock()
	if e, ok := c.entries[rawURL]; ok && c.now().Before(e.expires) {
		c.mu.Unlock()
		return e.kf, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxJWKSBodyBytes))
	if err != nil {
		return nil, err
	}
	kf, err := keyfunc.NewJWKSetJSON(body)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.entries[rawURL] = jwksCacheEntry{kf: kf, expires: c.now().Add(c.ttl)}
	c.mu.Unlock()
	return kf, nil
}

// validate enforces JWKS URL policy: reject anything that isn't
// `https://` unless allowInsecure is set; reject hosts not on the
// allowlist if one is configured.
func (p jwksPolicy) validate(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errInvalidJWKSURL
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		// always allowed
	case "http":
		if !p.allowInsecure {
			return errInvalidJWKSURL
		}
	default:
		return errInvalidJWKSURL
	}
	if u.Host == "" {
		return errInvalidJWKSURL
	}
	if len(p.allowedHosts) == 0 {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := p.allowedHosts[host]; !ok {
		return errInvalidJWKSURL
	}
	return nil
}

func normalizeHosts(hosts []string) map[string]struct{} {
	if len(hosts) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(strings.ToLower(h))
		if h == "" {
			continue
		}
		out[h] = struct{}{}
	}
	return out
}
