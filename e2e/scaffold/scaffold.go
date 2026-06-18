// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build scaffold

// Package scaffold exists solely to keep `go mod tidy` from pruning the
// project-wide dependency set listed in [ADR 0009 (Language Choice: Go)].
//
// The initial repo scaffold pinned 15 libraries the architecture commits
// to (chi router, prom client, JWT/JWKS, FHIR profile validator,
// otel/otlptrace, slog redaction, JCS canonicalizer source, etc.). The
// LLDs that own those deps have not landed yet, so nothing under
// `internal/` actually imports them. Without a reference here, the next
// `go mod tidy` would prune them and the next time an LLD agent reaches
// for `chi.Router` they'd have to re-add it — and re-debate the version.
//
// This file is excluded from every default build (the `scaffold` tag is
// not set anywhere). It compiles only when explicitly requested; that
// is enough for `go mod tidy` to see the imports and keep the
// dependencies in `go.mod`.
//
// Each LLD agent that lands its component will import its dep from the
// real package and we will trim the matching import from this file.
// When this file is empty, delete it.
//
// [ADR 0009 (Language Choice: Go)]: ../../docs/high-level-design/decisions/0009-language-choice.md
package scaffold

import (
	_ "github.com/BurntSushi/toml"
	_ "github.com/MicahParks/keyfunc/v3"
	_ "github.com/go-chi/chi/v5"
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/prometheus/client_golang/prometheus"
	_ "github.com/santhosh-tekuri/jsonschema/v5"
	_ "go.opentelemetry.io/otel"
	_ "go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	_ "golang.org/x/text/cases"
	_ "gopkg.in/yaml.v3"
	_ "pgregory.net/rapid"
)
