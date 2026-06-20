// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// TestNewSigner_GeneratesUsable2048RSA asserts the signer the binary
// uses at startup is real 2048-bit RSA, not a fake/canned value.
func TestNewSigner_GeneratesUsable2048RSA(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	if s.key == nil {
		t.Fatalf("signer has nil key")
	}
	if got := s.key.N.BitLen(); got < 2040 || got > 2048 {
		t.Fatalf("rsa modulus bitlen=%d; want 2048-ish", got)
	}
	if s.kid == "" {
		t.Fatalf("signer kid is empty")
	}
}

// TestJWKSDocument_RoundTripsAsRealKey asserts the JWKS document the
// binary publishes is real-key material (i.e. tokens signed with the
// private key verify against the JWKS-derived public key). This is the
// contract the prod fhir-subs verifier consumes.
func TestJWKSDocument_RoundTripsAsRealKey(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	doc := s.jwksDocument()

	// Reconstruct the public key from the JWKS document the binary
	// would have served, then verify a token we sign with the private
	// key validates against it.
	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(doc, &jwks); err != nil {
		t.Fatalf("decode JWKS: %v\n%s", err, doc)
	}
	if len(jwks.Keys) != 1 {
		t.Fatalf("JWKS has %d keys; want 1", len(jwks.Keys))
	}
	k := jwks.Keys[0]
	if k.Kty != "RSA" || k.Alg != "RS256" || k.Kid != s.kid {
		t.Fatalf("JWKS key mismatch: %+v vs signer kid %q", k, s.kid)
	}

	// Decode N and E and rebuild rsa.PublicKey.
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		t.Fatalf("decode N: %v", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		t.Fatalf("decode E: %v", err)
	}
	pub := &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: int(new(big.Int).SetBytes(eBytes).Int64()),
	}
	if pub.N.Cmp(s.key.N) != 0 {
		t.Fatalf("JWKS modulus does not match signer modulus")
	}
	if pub.E != s.key.E {
		t.Fatalf("JWKS exponent %d != signer exponent %d", pub.E, s.key.E)
	}

	// Sign a token, then verify with the rebuilt public key.
	tok, err := s.mint("https://issuer.example/", nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parsed, err := jwt.NewParser(jwt.WithValidMethods([]string{"RS256"})).Parse(tok, func(_ *jwt.Token) (any, error) {
		return pub, nil
	})
	if err != nil {
		t.Fatalf("parse signed token against JWKS-rebuilt pubkey: %v", err)
	}
	if !parsed.Valid {
		t.Fatalf("token reports invalid")
	}
}

// TestMint_DefaultClaimsAcceptable asserts the helper's default claim
// set includes everything the prod verifier requires (iss, aud, sub,
// client_id, jti, iat, exp, scope) and that exp is in the future.
func TestMint_DefaultClaimsAcceptable(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	tok, err := s.mint("https://issuer.example/", nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims := decodeClaimsForTest(t, tok)

	for _, k := range []string{"iss", "aud", "sub", "client_id", "jti", "iat", "exp", "scope"} {
		if _, ok := claims[k]; !ok {
			t.Errorf("default mint missing claim %q", k)
		}
	}
	if got, _ := claims["iss"].(string); got != "https://issuer.example/" {
		t.Errorf("iss=%q; want issuer arg", got)
	}
	exp, _ := claims["exp"].(float64)
	if int64(exp) < time.Now().Add(1*time.Minute).Unix() {
		t.Errorf("exp=%v; want at least 1m in the future", exp)
	}
}

// TestMint_OverridesWin asserts arbitrary claim overrides replace the
// default. This is the entire reason the helper exists: adversarial-
// auth tests need to produce tokens with deliberately-wrong claims.
func TestMint_OverridesWin(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	overrides := map[string]any{
		"iss":       "https://adversary.invalid/",
		"aud":       "wrong-audience",
		"client_id": "adversary-client",
		"jti":       "fixed-jti",
		"exp":       int64(123),
	}
	tok, err := s.mint("https://issuer.example/", overrides)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	claims := decodeClaimsForTest(t, tok)
	for k, want := range overrides {
		got := claims[k]
		if f, ok := got.(float64); ok {
			if w, ok := want.(int64); ok && int64(f) == w {
				continue
			}
		}
		if gotStr, _ := got.(string); gotStr != "" {
			if wantStr, _ := want.(string); wantStr != "" && gotStr == wantStr {
				continue
			}
		}
		t.Errorf("override %s: got %v, want %v", k, got, want)
	}
}

// TestMint_KidMatchesJWKS asserts the JWT header's kid equals the JWKS
// document's kid — the verifier resolves keys by header.kid, so a
// mismatch silently breaks every verification.
func TestMint_KidMatchesJWKS(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	tok, err := s.mint("https://issuer.example/", nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token not in JWT format: %q", tok)
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		t.Fatalf("decode header JSON: %v", err)
	}
	if got, _ := hdr["kid"].(string); got != s.kid {
		t.Errorf("token header.kid=%q; want %q", got, s.kid)
	}
	if got, _ := hdr["alg"].(string); got != "RS256" {
		t.Errorf("token header.alg=%q; want RS256", got)
	}
}

// TestJWKSCompatibleWithKeyfunc asserts the JWKS document the binary
// publishes is consumable by the same MicahParks/keyfunc library the
// prod verifier uses (verifier.go imports it). Pinning this contract
// catches a future signer-format drift before it breaks the verifier.
func TestJWKSCompatibleWithKeyfunc(t *testing.T) {
	s, err := newSigner()
	if err != nil {
		t.Fatalf("newSigner: %v", err)
	}
	jwksJSON := s.jwksDocument()
	kf, err := keyfunc.NewJWKSetJSON(jwksJSON)
	if err != nil {
		t.Fatalf("keyfunc.NewJWKSetJSON: %v", err)
	}
	tok, err := s.mint("https://issuer.example/", nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parsed, err := jwt.NewParser(
		jwt.WithValidMethods([]string{"RS256"}),
	).Parse(tok, kf.Keyfunc)
	if err != nil {
		t.Fatalf("parse via keyfunc: %v", err)
	}
	if !parsed.Valid {
		t.Fatalf("token invalid via keyfunc path")
	}
}

func decodeClaimsForTest(t *testing.T, tok string) map[string]any {
	t.Helper()
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token not in JWT format: %q", tok)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return claims
}
