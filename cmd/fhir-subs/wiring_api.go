// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"
	"strings"
)

// derivePublicURLs returns (BaseURL, WSBaseURL) per the rules in story #179:
//
//   - When cfg.API.BaseURL is non-empty, it wins. The same goes for
//     cfg.API.WSBaseURL. Operators with a public DNS name in front of
//     the pod set these so CapabilityStatement.url renders the
//     externally-routable URL, not the in-cluster bind.
//
//   - Otherwise, scheme is derived from cfg.Server.HTTP.Insecure: true
//     ⇒ http / ws, false ⇒ https / wss. Host is cfg.Server.HTTP.Bind
//     verbatim. The legacy wiring hardcoded https:// regardless of
//     insecure, which broke local dev (kubelet probes a TLS-less pod
//     and gets x509 errors) and broke conformance probes pointing at
//     the http listener.
func derivePublicURLs(cfg *Config) (baseURL, wsBaseURL string) {
	httpScheme := "https"
	wsScheme := "wss"
	if cfg.Server.HTTP.Insecure {
		httpScheme = "http"
		wsScheme = "ws"
	}

	baseURL = cfg.API.BaseURL
	if baseURL == "" {
		baseURL = httpScheme + "://" + cfg.Server.HTTP.Bind
	}

	wsBaseURL = cfg.API.WSBaseURL
	if wsBaseURL == "" {
		wsBaseURL = wsScheme + "://" + cfg.Server.HTTP.Bind + "/ws"
	}
	return baseURL, wsBaseURL
}

// deriveJWKSURL returns the JWKS document URL the binary advertises in
// CapabilityStatement.security and serves at /.well-known/jwks.json.
//
// When configured (cfg.API.JWKSURL non-empty), the operator value wins
// — supports the case where a public sidecar hosts the JWKS under a
// different DNS name. Otherwise we derive {baseURL}/.well-known/jwks.json
// so the URL we advertise matches the path we actually mount.
func deriveJWKSURL(configured, baseURL string) string {
	if configured != "" {
		return configured
	}
	if baseURL == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/.well-known/jwks.json"
}

// newJWKSHandler returns the http.Handler mounted at /.well-known/jwks.json
// (story #181). Today the production binary signs access tokens with
// HS256 (a shared secret); HS256 keys cannot be published. The handler
// therefore serves an empty JSON Web Key Set, which is the canonical
// response for a server that does not publish verification keys.
//
// Returning the empty set (rather than 404) matches the SMART-on-FHIR
// "JWKS endpoint MUST be reachable" expectation: clients that resolve
// the URL get a parseable document and learn there is no asymmetric
// key to verify against, instead of a 404 they have to special-case.
//
// Future work: when the binary grows asymmetric token signing
// (RS256/ES256), this handler reads the active public key from the
// auth.TokenEndpoint and renders it here.
func newJWKSHandler() http.Handler {
	const body = `{"keys":[]}`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	})
}
