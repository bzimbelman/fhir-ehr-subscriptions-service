// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Command test-token-mint is a real RSA-signing JWT helper for
// realstack tests. It runs inside the e2e/realstack docker-compose
// stack and lets adversarial-auth tests mint tokens with arbitrary
// claim overrides without round-tripping through Keycloak.
//
// At startup the binary generates a real 2048-bit RSA keypair (no
// shared/canned key) and serves three endpoints:
//
//	GET  /healthz       — liveness probe.
//	GET  /jwks.json     — public key as a JWKS document. The prod
//	                      fhir-subs binary's verifier fetches this
//	                      via the per-client auth_clients.jwks_url
//	                      lookup the realstack harness provisions.
//	POST /mint          — request body: JSON object of claim overrides
//	                      to merge over the default claim set.
//	                      Response: {"token": "<JWT>", "kid": "..."}.
//
// Default claims (overridable per /mint call):
//
//	iss        — http://test-token-mint:8092/  (the binary's issuer URL)
//	aud        — fhir-subs-test                 (matches realstack auth.audience)
//	sub        — realstack-test-mint            (echoed as default client_id)
//	client_id  — realstack-test-mint
//	jti        — random 16-byte hex
//	iat        — now
//	exp        — now + 5m
//	scope      — system/Subscription.cruds system/Subscription.r system/Subscription.s
//
// The binary lives ONLY in the e2e/realstack/docker-compose.yml file
// and is never deployed to production. The realstack harness gates the
// helper behind auth.allow_dev_bypass=true and deployment.environment=test
// in the rendered prod-binary config (see boot.go), so even an
// accidental copy of the helper image cannot be reached from a config
// that has not opted into dev-bypass.
//
// Build tag: none — the binary builds in the normal go-build path
// because the realstack docker-compose.yml builds it as part of the
// stack image set. The binary is harmless on a host that doesn't reach
// it (it just listens on its bound port).
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// signer holds the RSA keypair the binary uses to sign every minted
// token, plus the kid the JWKS exposes.
type signer struct {
	key *rsa.PrivateKey
	kid string
}

func newSigner() (*signer, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate rsa: %w", err)
	}
	// kid is a deterministic SHA-256 of the public modulus so the
	// realstack harness can correlate a JWKS fetch with a fresh boot.
	hash := sha256.Sum256(priv.PublicKey.N.Bytes())
	return &signer{key: priv, kid: hex.EncodeToString(hash[:8])}, nil
}

// jwksDocument returns the public key as a JWKS-format JSON document.
// Format: {"keys":[{"kty":"RSA","kid":"...","alg":"RS256","use":"sig","n":"...","e":"..."}]}.
func (s *signer) jwksDocument() []byte {
	pub := s.key.PublicKey
	doc := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": s.kid,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			},
		},
	}
	body, _ := json.Marshal(doc)
	return body
}

// defaultClaims returns the claim set the helper applies before
// overrides. iss/aud/sub/client_id/scope match what the realstack
// harness configures the prod binary to accept; jti/iat/exp are
// randomized per call so two consecutive default mints don't replay.
func defaultClaims(issuer string) jwt.MapClaims {
	now := time.Now()
	jtiBytes := make([]byte, 16)
	_, _ = rand.Read(jtiBytes)
	return jwt.MapClaims{
		"iss":       issuer,
		"aud":       "fhir-subs-test",
		"sub":       "realstack-test-mint",
		"client_id": "realstack-test-mint",
		"jti":       hex.EncodeToString(jtiBytes),
		"iat":       now.Unix(),
		"exp":       now.Add(5 * time.Minute).Unix(),
		"scope":     "system/Subscription.cruds system/Subscription.r system/Subscription.s",
	}
}

// mintRequest is the JSON body /mint accepts. The Overrides map is
// merged over the default claims. nil/empty body → all defaults.
type mintRequest struct {
	Overrides map[string]any `json:"overrides"`
}

// mintResponse is the JSON body /mint returns.
type mintResponse struct {
	Token string `json:"token"`
	Kid   string `json:"kid"`
}

func (s *signer) mint(issuer string, overrides map[string]any) (string, error) {
	claims := defaultClaims(issuer)
	for k, v := range overrides {
		claims[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = s.kid
	return tok.SignedString(s.key)
}

func main() {
	addr := flag.String("addr", ":8092", "HTTP listener address")
	issuer := flag.String("issuer", "http://test-token-mint:8092/", "iss claim default and JWKS issuer field")
	flag.Parse()

	sig, err := newSigner()
	if err != nil {
		log.Fatalf("newSigner: %v", err)
	}

	var mu sync.Mutex
	jwksDoc := sig.jwksDocument()

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})

	mux.HandleFunc("/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := jwksDoc
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})

	mux.HandleFunc("/mint", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var req mintRequest
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if uerr := json.Unmarshal(body, &req); uerr != nil {
				http.Error(w, "decode body: "+uerr.Error(), http.StatusBadRequest)
				return
			}
		}
		tok, err := sig.mint(*issuer, req.Overrides)
		if err != nil {
			http.Error(w, "sign: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(mintResponse{Token: tok, Kid: sig.kid})
	})

	log.Printf("test-token-mint listening on %s, issuer=%s, kid=%s", *addr, *issuer, sig.kid)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
