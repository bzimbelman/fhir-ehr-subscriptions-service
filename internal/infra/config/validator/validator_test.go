// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package validator_test

import (
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/schemas"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/validator"
)

// TestStructuralBeforeSecret: an unset env var referenced by a placeholder is
// NOT touched if structural validation fails first. The validator's
// ValidateStructural method must run before secret resolution.
//
// We exercise this by giving the validator a tree with both a structural
// problem (missing required field) AND a placeholder. The validator must
// surface the structural error and never try to resolve the placeholder.
func TestStructuralBeforeSecret(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()

	// Tree missing the required deployment.facility_id and with a placeholder
	// referencing a definitely-unset env var.
	tree := map[string]interface{}{
		"deployment": map[string]interface{}{
			// facility_id missing — should fail structural
		},
		"adapter": map[string]interface{}{
			"id": "${env:DEFINITELY_NOT_SET_FHIR_VALIDATOR_TEST}",
		},
	}
	report := validator.ValidateStructural(tree, r)
	if report.OK() {
		t.Fatalf("expected structural failure, got OK")
	}
	// The error report must mention deployment.facility_id but NOT the env-var
	// substitution path (we never tried to resolve it).
	msg := report.Error()
	if !strings.Contains(msg, "facility_id") {
		t.Fatalf("error must mention facility_id; got %v", msg)
	}
	if strings.Contains(msg, "DEFINITELY_NOT_SET_FHIR_VALIDATOR_TEST") {
		t.Fatalf("validator must not touch placeholder values; got %v", msg)
	}
}

// TestStructuralOK: a complete-enough tree passes structural.
func TestStructuralOK(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	tree := goodMinimalTree()
	report := validator.ValidateStructural(tree, r)
	if !report.OK() {
		t.Fatalf("expected OK; got %v", report.Error())
	}
}

// TestUnknownKeyRejected per LLD §13: "Unknown key in the config file —
// structural validation rejects with `unknown key <path>`. No silent typos."
func TestUnknownKeyRejected(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	tree := goodMinimalTree()
	dep := tree["deployment"].(map[string]interface{})
	dep["facillity_id"] = "typo" // misspelled — structural reject expected
	report := validator.ValidateStructural(tree, r)
	if report.OK() {
		t.Fatalf("expected unknown-key failure")
	}
	if !strings.Contains(report.Error(), "facillity_id") {
		t.Fatalf("error must mention typo'd key; got %v", report.Error())
	}
}

// TestSchemaRegistration: a manifest-registered domain validates via the
// validator like core domains.
func TestSchemaRegistration(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	manifestSchema := []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type": "object",
		"properties": {
			"fhir_base_url": {"type": "string", "format": "uri"}
		},
		"required": ["fhir_base_url"]
	}`)
	if err := r.Register("adapter.config", manifestSchema); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tree := goodMinimalTree()
	tree["adapter"] = map[string]interface{}{
		"id":     "epic",
		"config": map[string]interface{}{
			// fhir_base_url missing — manifest validation should fail
		},
	}
	report := validator.ValidateDomainSchemas(tree, r)
	if report.OK() {
		t.Fatalf("expected adapter.config schema failure")
	}
	if !strings.Contains(report.Error(), "fhir_base_url") {
		t.Fatalf("error must mention missing fhir_base_url; got %v", report.Error())
	}
}

// TestSemanticAuthRequired: the architecture says "you cannot run with no
// auth"; validator surfaces this even when both auth.trusted_issuers and
// auth.client_registry are empty.
func TestSemanticAuthRequired(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	tree := goodMinimalTree()
	auth := tree["auth"].(map[string]interface{})
	auth["trusted_issuers"] = []interface{}{}
	auth["client_registry"] = []interface{}{}
	report := validator.ValidateSemantic(tree, r)
	if report.OK() {
		t.Fatalf("expected auth-required failure")
	}
	if !strings.Contains(report.Error(), "auth") {
		t.Fatalf("error must mention auth; got %v", report.Error())
	}
}

// TestSemanticListenerPortCollision per LLD §13.
func TestSemanticListenerPortCollision(t *testing.T) {
	t.Parallel()
	r := schemas.NewRegistry()
	tree := goodMinimalTree()
	tree["mllp_listener"] = map[string]interface{}{
		"endpoints": []interface{}{
			map[string]interface{}{"name": "a", "bind": "0.0.0.0:2575"},
			map[string]interface{}{"name": "b", "bind": "0.0.0.0:2575"},
		},
	}
	report := validator.ValidateSemantic(tree, r)
	if report.OK() {
		t.Fatalf("expected port collision failure")
	}
	if !strings.Contains(report.Error(), "2575") {
		t.Fatalf("error must mention the colliding port; got %v", report.Error())
	}
}

// goodMinimalTree returns a tree that satisfies hard-required fields (per
// "What's required vs optional" in the architecture).
func goodMinimalTree() map[string]interface{} {
	return map[string]interface{}{
		"deployment": map[string]interface{}{
			"facility_id": "memorial-east",
			"log_level":   "info",
			"environment": "production",
			"log_format":  "json",
		},
		"server": map[string]interface{}{
			"http": map[string]interface{}{
				"bind": "0.0.0.0:8443",
			},
		},
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"url": "postgres://localhost/db",
			},
		},
		"auth": map[string]interface{}{
			"trusted_issuers": []interface{}{
				map[string]interface{}{
					"issuer":   "https://idp.example.org",
					"jwks_url": "https://idp.example.org/.well-known/jwks.json",
					"audience": "https://fhir-subs.example.org",
				},
			},
		},
		"adapter": map[string]interface{}{
			"id": "epic",
		},
	}
}
