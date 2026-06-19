// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// buildAuthEndpoints constructs the verifier and token endpoint from
// config. Returns (nil, nil, nil) when audience is empty (auth disabled
// — not allowed in production but useful for the probe-only fallback).
func buildAuthEndpoints(cfg AuthConfig, pool *pgxpool.Pool, clients *repos.AuthClientsRepo) (*auth.Verifier, *auth.TokenEndpoint, error) {
	if cfg.Audience == "" {
		return nil, nil, nil
	}
	lookup := &poolClientLookup{pool: pool, repo: clients}

	var issuedSecret []byte
	if cfg.IssuedSecret != "" {
		decoded, err := base64.StdEncoding.DecodeString(cfg.IssuedSecret)
		if err != nil {
			return nil, nil, fmt.Errorf("issued_secret base64 decode: %w", err)
		}
		issuedSecret = decoded
	}

	verif, err := auth.NewVerifier(auth.VerifierConfig{
		Audience:          cfg.Audience,
		ClockSkew:         cfg.ClockSkew,
		ClientLookup:      lookup,
		JWKSCacheTTL:      cfg.JWKSCacheTTL,
		IssuedSecret:      issuedSecret,
		IssuedIssuer:      cfg.IssuedIssuer,
		AllowInsecureJWKS: cfg.AllowInsecure,
		JWKSAllowedHosts:  cfg.JWKSAllowed,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("verifier: %w", err)
	}

	if cfg.TokenURL == "" || len(issuedSecret) == 0 {
		return verif, nil, nil
	}
	te, err := auth.NewTokenEndpoint(auth.TokenEndpointConfig{
		Audience:          cfg.Audience,
		TokenURL:          cfg.TokenURL,
		AccessTokenSecret: issuedSecret,
		AccessTokenTTL:    cfg.AccessTokenTTL,
		AccessTokenIssuer: cfg.IssuedIssuer,
		ClientLookup:      lookup,
		JWKSCacheTTL:      cfg.JWKSCacheTTL,
		ClockSkew:         cfg.ClockSkew,
		AllowInsecureJWKS: cfg.AllowInsecure,
		JWKSAllowedHosts:  cfg.JWKSAllowed,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("token endpoint: %w", err)
	}
	return verif, te, nil
}

// poolClientLookup adapts repos.AuthClientsRepo to the auth.ClientLookup
// interface. The verifier's ClientLookup signature is (ctx, id) — repos
// take an explicit Querier — so this thin wrapper supplies the pool.
type poolClientLookup struct {
	pool *pgxpool.Pool
	repo *repos.AuthClientsRepo
}

func (p *poolClientLookup) GetByID(ctx context.Context, id string) (*auth.ClientRecord, error) {
	row, err := p.repo.GetByID(ctx, p.pool, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return &auth.ClientRecord{
		ID:      row.ID,
		JwksURL: row.JwksURL,
		Scopes:  row.Scopes,
	}, nil
}

// devPrincipalMiddleware reads the X-Client-Id header and, when
// present, attaches a Principal to the request context with a
// permissive scope set covering every operator-mintable scope used by
// the API. The scope set mirrors the auth_clients.scopes column so a
// dev request behaves exactly like an authenticated client whose
// scopes were granted out-of-band.
//
// This is wired ONLY when cfg.Auth.Audience is empty (the dev
// /e2e fallback path). Production deployments MUST set audience so the
// real verifier is installed instead. Empty header still produces a
// 401 from mustPrincipal — the dev path does not invent identities.
func devPrincipalMiddleware() handlers.Middleware {
	scopes := []string{
		"system/Subscription.cruds",
		"system/Subscription.r",
		"system/Subscription.s",
		"system/Subscription.c",
		"system/Subscription.u",
		"system/Subscription.d",
		"system/Subscription.$status",
		"system/Subscription.$events",
		"system/Subscription.$get-ws-binding-token",
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cid := r.Header.Get("X-Client-Id")
			if cid == "" {
				next.ServeHTTP(w, r)
				return
			}
			p := &auth.Principal{ClientID: cid, Scopes: scopes}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
		})
	}
}

// defaultActivator is the production placeholder for per-channel
// activation handshakes. It always returns HandshakeSucceeded so the
// API flips a freshly-created subscription from `requested` to
// `active`. Real handshake plumbing (FHIR R5 handshake bundle to
// rest-hook subscribers, etc.) is owned by each channel module's own
// activation logic and replaces this default in a follow-up. Without
// the placeholder, every newly created subscription would stay stuck at
// `requested` and the submatcher's "active subscriptions" filter would
// never see it.
type defaultActivator struct{}

// ActivateSubscription always succeeds. See the type-level comment for
// why this is the production default today.
func (defaultActivator) ActivateSubscription(_ context.Context, _ repos.SubscriptionRow) (handlers.HandshakeOutcome, error) {
	return handlers.HandshakeSucceeded, nil
}
