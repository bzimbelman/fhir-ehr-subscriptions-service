// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
)

// applyAdminDeps validates the admin shared secret and assigns the
// admin-surface fields on handlers.Deps. Story #92: the token-length
// check fails closed at startup so a misconfigured operator cannot
// deploy with a too-short admin token; #285 lifted this from
// buildProductionRuntime so admin wiring lives in a single file.
//
// `dl` is the dead-letters repo used to back the admin dead-letters
// view; it is owned by buildProductionRuntime and threaded in here so
// this helper does not duplicate repo construction.
func applyAdminDeps(deps *handlers.Deps, cfg AdminConfig, pool *pgxpool.Pool, dl *repos.DeadLettersRepo) error {
	if cfg.Token != "" && len(cfg.Token) < handlers.MinAdminTokenBytes {
		return fmt.Errorf("admin: token must be at least %d bytes (got %d)",
			handlers.MinAdminTokenBytes, len(cfg.Token))
	}
	deps.AdminToken = cfg.Token
	deps.AdminPathPrefix = cfg.PathPrefix
	deps.DeadLetters = handlers.NewPgDeadLettersStore(pool, dl)
	deps.AdminRateLimit = auth.NewClientRateLimiter(auth.RateLimit{
		Burst:           cfg.RateLimit.Burst,
		RefillPerSecond: cfg.RateLimit.RefillPerSecond,
		MaxKeys:         cfg.RateLimit.MaxKeys,
	}, nil)
	return nil
}
