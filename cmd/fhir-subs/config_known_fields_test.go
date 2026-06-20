// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

// OP #205 RED — loadConfig MUST fail loud on unknown top-level keys so
// an operator typo (e.g. `databse:` instead of `database:`) cannot
// silently land in cfg.Extra and ship to production. The fix is
// `dec.KnownFields(true)` on the yaml.Decoder at config.go:610. Today
// the decoder is permissive: typos are not rejected. The Extra
// `yaml:",inline"` field also catches them, so a stray top-level key
// is invisibly accepted.
//
// This test writes a YAML with a typo'd top-level key, calls
// loadConfig, and asserts a non-nil error mentioning the unknown
// field. It also verifies the happy-path: a YAML with all valid known
// fields still loads cleanly.

func TestLoadConfig_RejectsUnknownTopLevelKey(t *testing.T) {
	t.Parallel()
	// `databse` (sic) is a common typo for `database`. Today the YAML
	// decoder accepts it silently (Extra absorbs it). AC: reject.
	yaml := `
deployment:
  facility_id: f1
adapter:
  id: default
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
databse:
  url: postgres://typo.invalid/db?sslmode=disable
auth:
  audience: https://example.invalid
topics:
  catalog_dir: /tmp/topics
`
	path := writeTempYAML(t, yaml)
	cfg, err := loadConfig(path)
	if err == nil {
		t.Fatalf("loadConfig succeeded; expected error mentioning unknown 'databse' field. cfg.Extra=%v", cfg.Extra)
	}
	// The yaml.Decoder error from KnownFields(true) is shaped like
	// "field databse not found in type ..." — be lenient about the
	// exact wording but require the typo'd key name to appear.
	if !strings.Contains(err.Error(), "databse") {
		t.Errorf("error %q does not mention the unknown field 'databse'; AC requires the message to identify the offending key", err.Error())
	}
}

func TestLoadConfig_AcceptsValidKnownFields(t *testing.T) {
	t.Parallel()
	yaml := `
deployment:
  facility_id: f1
adapter:
  id: default
server:
  http:
    bind: 127.0.0.1:0
    insecure: true
database:
  url: postgres://example.invalid/db?sslmode=disable
auth:
  audience: https://example.invalid
topics:
  catalog_dir: /tmp/topics
`
	path := writeTempYAML(t, yaml)
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig failed on valid YAML: %v", err)
	}
	if cfg.Database.URL == "" {
		t.Errorf("expected database.url to be parsed, got empty")
	}
}

// writeTempYAML is provided by config_test.go in the same package.
