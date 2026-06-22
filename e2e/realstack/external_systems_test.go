// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

// OP #346 (RED): unit-level tests for the external-systems env-gating
// helper. These tests pin the ParseExternalSystemConfig contract that
// Boot consults to decide whether to activate the docker-compose
// "external-local" profile or skip it because the operator has set all
// three env vars (FHIR_SUBS_TEST_DB_URL, FHIR_SUBS_TEST_FHIR_URL,
// FHIR_SUBS_TEST_OIDC_ISSUER_URL).
//
// These tests do NOT require docker — they exercise pure Go code. They
// live behind the e2e_realstack build tag because the helper itself
// lives behind that tag (the realstack package is gated end-to-end).

package realstack

import (
	"testing"
)

// TestParseExternalSystemConfig_AllUnsetActivatesProfile asserts that
// when none of the three env vars are set the helper returns a config
// whose UseExternal flag is false (Boot will activate the
// "external-local" compose profile so postgres, keycloak, and
// hapi-fhir come up locally).
func TestParseExternalSystemConfig_AllUnsetActivatesProfile(t *testing.T) {
	cfg, err := ParseExternalSystemConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("ParseExternalSystemConfig returned error: %v", err)
	}
	if cfg.UseExternal {
		t.Fatalf("UseExternal=true with no env vars set; want false")
	}
}

// TestParseExternalSystemConfig_AllSetSkipsProfile asserts that when
// all three env vars are set the helper returns UseExternal=true with
// the values populated. Boot will skip the compose profile and use the
// supplied URLs.
func TestParseExternalSystemConfig_AllSetSkipsProfile(t *testing.T) {
	getenv := mapGetenv(map[string]string{
		"FHIR_SUBS_TEST_DB_URL":          "postgres://u:p@db.example.com:5432/fhirsubs?sslmode=disable",
		"FHIR_SUBS_TEST_FHIR_URL":        "https://hapi.example.com/fhir",
		"FHIR_SUBS_TEST_OIDC_ISSUER_URL": "https://kc.example.com/realms/fhir-subs",
	})
	cfg, err := ParseExternalSystemConfig(getenv)
	if err != nil {
		t.Fatalf("ParseExternalSystemConfig returned error: %v", err)
	}
	if !cfg.UseExternal {
		t.Fatalf("UseExternal=false when all three env vars set; want true")
	}
	if cfg.DBURL != "postgres://u:p@db.example.com:5432/fhirsubs?sslmode=disable" {
		t.Errorf("DBURL=%q; want the value supplied via env", cfg.DBURL)
	}
	if cfg.FHIRBaseURL != "https://hapi.example.com/fhir" {
		t.Errorf("FHIRBaseURL=%q; want the value supplied via env", cfg.FHIRBaseURL)
	}
	if cfg.OIDCIssuerURL != "https://kc.example.com/realms/fhir-subs" {
		t.Errorf("OIDCIssuerURL=%q; want the value supplied via env", cfg.OIDCIssuerURL)
	}
}

// TestParseExternalSystemConfig_PartialMixIsError asserts that any
// single env var set without the other two yields an error. v1 of the
// env-gate explicitly does not support mix-and-match: either all three
// are external or all three come up locally. The error names the
// missing variables so the operator can fix their environment without
// reading the source.
func TestParseExternalSystemConfig_PartialMixIsError(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		wantNames []string
	}{
		{
			name: "only_db_set",
			env: map[string]string{
				"FHIR_SUBS_TEST_DB_URL": "postgres://x",
			},
			wantNames: []string{"FHIR_SUBS_TEST_FHIR_URL", "FHIR_SUBS_TEST_OIDC_ISSUER_URL"},
		},
		{
			name: "only_fhir_set",
			env: map[string]string{
				"FHIR_SUBS_TEST_FHIR_URL": "http://hapi/fhir",
			},
			wantNames: []string{"FHIR_SUBS_TEST_DB_URL", "FHIR_SUBS_TEST_OIDC_ISSUER_URL"},
		},
		{
			name: "only_issuer_set",
			env: map[string]string{
				"FHIR_SUBS_TEST_OIDC_ISSUER_URL": "https://kc/realms/r",
			},
			wantNames: []string{"FHIR_SUBS_TEST_DB_URL", "FHIR_SUBS_TEST_FHIR_URL"},
		},
		{
			name: "two_of_three_set",
			env: map[string]string{
				"FHIR_SUBS_TEST_DB_URL":   "postgres://x",
				"FHIR_SUBS_TEST_FHIR_URL": "http://hapi/fhir",
			},
			wantNames: []string{"FHIR_SUBS_TEST_OIDC_ISSUER_URL"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseExternalSystemConfig(mapGetenv(tc.env))
			if err == nil {
				t.Fatalf("ParseExternalSystemConfig returned no error for partial mix %v; cfg=%+v", tc.env, cfg)
			}
			for _, name := range tc.wantNames {
				if !containsString(err.Error(), name) {
					t.Errorf("error %q does not name missing var %q; operator needs that hint", err, name)
				}
			}
		})
	}
}

// TestParseExternalSystemConfig_BlankIsUnset asserts that whitespace-
// only env values are treated as unset. Operators frequently export
// vars to "" when a parent shell has them defined; the helper must not
// silently accept those as valid URLs.
func TestParseExternalSystemConfig_BlankIsUnset(t *testing.T) {
	getenv := mapGetenv(map[string]string{
		"FHIR_SUBS_TEST_DB_URL":          "   ",
		"FHIR_SUBS_TEST_FHIR_URL":        "https://hapi.example.com/fhir",
		"FHIR_SUBS_TEST_OIDC_ISSUER_URL": "https://kc.example.com/realms/fhir-subs",
	})
	if _, err := ParseExternalSystemConfig(getenv); err == nil {
		t.Fatal("ParseExternalSystemConfig accepted whitespace-only DB URL; want error naming FHIR_SUBS_TEST_DB_URL")
	} else if !containsString(err.Error(), "FHIR_SUBS_TEST_DB_URL") {
		t.Errorf("error %q does not name the blank var FHIR_SUBS_TEST_DB_URL", err)
	}
}

// mapGetenv adapts a map literal into the os.Getenv-compatible function
// shape ParseExternalSystemConfig accepts. Lets tests inject
// environments without mutating process state.
func mapGetenv(m map[string]string) func(string) string {
	return func(k string) string {
		return m[k]
	}
}

// containsString is a tiny strings.Contains shim so the test file does
// not introduce a dependency on the strings package solely for its one
// use site.
func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
