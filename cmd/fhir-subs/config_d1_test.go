// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"
)

// TestLoadConfig_D1_TopicsBlock asserts that the config loader understands
// the `topics.catalog_dir` block the production wiring uses to populate the
// matcher's CatalogProvider. Before D-1, `topics.*` was unmodeled, so the
// matcher silently received an empty catalog regardless of operator intent.
//
// D-1.
func TestLoadConfig_D1_TopicsBlock(t *testing.T) {
	t.Parallel()

	yaml := `
deployment:
  facility_id: hospital-a
adapter:
  id: default
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
lifecycle:
  shutdown_grace_period: 30s
topics:
  catalog_dir: /etc/fhir-subs/topics
`
	p := writeTempYAML(t, yaml)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Topics.CatalogDir != "/etc/fhir-subs/topics" {
		t.Errorf("topics.catalog_dir: got %q, want %q",
			cfg.Topics.CatalogDir, "/etc/fhir-subs/topics")
	}
}

// TestApplySets_D1_TopicsCatalogDir lets operators override the topic
// catalog directory at the CLI / env layer without rewriting the YAML.
//
// D-1.
func TestApplySets_D1_TopicsCatalogDir(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if err := applySets(cfg, []string{"topics.catalog_dir=/tmp/topics"}); err != nil {
		t.Fatalf("applySets: %v", err)
	}
	if cfg.Topics.CatalogDir != "/tmp/topics" {
		t.Errorf("topics.catalog_dir: got %q, want /tmp/topics", cfg.Topics.CatalogDir)
	}
}
