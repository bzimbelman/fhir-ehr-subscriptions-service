// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package realstack

import (
	"fmt"
	"sort"
	"strings"
)

// ExternalSystemConfig captures the operator's intent for the three
// shared infrastructure dependencies the realstack harness needs:
// Postgres, HAPI FHIR, and Keycloak (OIDC issuer).
//
// Two modes are supported:
//
//  1. Local: every field is empty and UseExternal is false. Boot
//     activates the docker-compose "external-local" profile so all
//     three services come up as containers.
//
//  2. External: every field is populated and UseExternal is true. Boot
//     skips the "external-local" profile and points the production
//     binary at the URLs supplied here. This is the path used to point
//     the harness at zdock or a shared cluster instance.
//
// Partial mixes (one or two env vars set) are rejected at parse time —
// see ParseExternalSystemConfig for the error contract.
type ExternalSystemConfig struct {
	// UseExternal is true when all three URLs are populated. Boot reads
	// this to decide whether to activate the "external-local" compose
	// profile.
	UseExternal bool

	// DBURL is the Postgres DSN the harness hands to the production
	// binary's database.url config field. Format:
	// postgres://user:pass@host:port/db?sslmode=...
	DBURL string

	// FHIRBaseURL is the HAPI FHIR base URL. Format: scheme://host:port/fhir
	// (the trailing /fhir is the conventional HAPI mount; the harness
	// does not rewrite the value).
	FHIRBaseURL string

	// OIDCIssuerURL is the Keycloak realm URL the production binary's
	// verifier uses to discover the JWKS endpoint. Format:
	// scheme://host:port/realms/<realm-name>
	OIDCIssuerURL string
}

// envVarDBURL is the name of the env var that supplies the external
// Postgres DSN.
const envVarDBURL = "FHIR_SUBS_TEST_DB_URL"

// envVarFHIRURL is the name of the env var that supplies the external
// HAPI FHIR base URL.
const envVarFHIRURL = "FHIR_SUBS_TEST_FHIR_URL"

// envVarOIDCIssuerURL is the name of the env var that supplies the
// external Keycloak realm issuer URL.
const envVarOIDCIssuerURL = "FHIR_SUBS_TEST_OIDC_ISSUER_URL"

// ParseExternalSystemConfig reads the three external-system env vars
// via the supplied getenv (os.Getenv-shaped) and returns the resulting
// ExternalSystemConfig.
//
// Decision table:
//
//	all unset       -> {UseExternal: false}, no error
//	all set         -> {UseExternal: true, ...}, no error
//	any partial mix -> error naming every UNSET var
//
// The error path lists the missing variables explicitly so the
// operator knows exactly what to set without reading source. v1 of the
// env-gate intentionally rejects partial mixes; if the use case
// emerges later (e.g. external Postgres but local Keycloak), file a
// follow-up story rather than relax this check silently.
//
// Whitespace-only values are treated as unset. Operators commonly
// export an env var as "" from a parent shell; honoring those as
// "valid URLs" would silently break Boot.
func ParseExternalSystemConfig(getenv func(string) string) (ExternalSystemConfig, error) {
	values := map[string]string{
		envVarDBURL:         strings.TrimSpace(getenv(envVarDBURL)),
		envVarFHIRURL:       strings.TrimSpace(getenv(envVarFHIRURL)),
		envVarOIDCIssuerURL: strings.TrimSpace(getenv(envVarOIDCIssuerURL)),
	}

	var setNames, unsetNames []string
	for name, val := range values {
		if val == "" {
			unsetNames = append(unsetNames, name)
		} else {
			setNames = append(setNames, name)
		}
	}
	sort.Strings(setNames)
	sort.Strings(unsetNames)

	switch len(setNames) {
	case 0:
		// All unset — local mode.
		return ExternalSystemConfig{UseExternal: false}, nil
	case 3:
		// All set — external mode.
		return ExternalSystemConfig{
			UseExternal:   true,
			DBURL:         values[envVarDBURL],
			FHIRBaseURL:   values[envVarFHIRURL],
			OIDCIssuerURL: values[envVarOIDCIssuerURL],
		}, nil
	default:
		// Partial mix — reject loudly with the missing names.
		return ExternalSystemConfig{}, fmt.Errorf(
			"realstack: external-system env vars must be all-set or all-unset; "+
				"set=%s, missing=%s. v1 of the env-gate does not support "+
				"partial mixes (file a follow-up story if you need it)",
			strings.Join(setNames, ","),
			strings.Join(unsetNames, ","),
		)
	}
}
