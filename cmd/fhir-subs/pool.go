// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// defaultPgxConnectTimeout is the per-attempt connect deadline applied
// when the operator did not configure one. Picked to surface a closed-
// port misconfiguration before pgxpool's internal retry loop outruns
// the caller's startup budget (D-3).
const defaultPgxConnectTimeout = 5 * time.Second

// buildPoolConfig parses databaseURL into a pgxpool.Config and overrides
// ConnConfig.ConnectTimeout so the per-attempt dial honors the
// operator-supplied bound (D-3). Without this, an unreachable Postgres
// can keep the binary in pgxpool's connect-retry loop past the
// startup-pingCtx and the operator-facing diagnostic surfaces only after
// the lifecycle module's signal-driven shutdown phase.
func buildPoolConfig(databaseURL string, connectTimeout time.Duration) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("database: parse url: %w", err)
	}
	if connectTimeout <= 0 {
		connectTimeout = defaultPgxConnectTimeout
	}
	cfg.ConnConfig.ConnectTimeout = connectTimeout
	return cfg, nil
}
