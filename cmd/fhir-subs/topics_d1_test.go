// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadTopicSources_D1_EmptyDirReturnsNoFiles asserts that an empty
// (or absent) topics.catalog_dir yields an empty Sources block without
// error. Operators who haven't published topics yet still boot.
func TestLoadTopicSources_D1_EmptyDirReturnsNoFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src, err := loadTopicSources(dir)
	if err != nil {
		t.Fatalf("loadTopicSources empty dir: %v", err)
	}
	if len(src.Operator) != 0 {
		t.Errorf("operator topics: got %d, want 0", len(src.Operator))
	}
}

// TestLoadTopicSources_D1_AbsentDirIsHandled asserts that a missing
// topics.catalog_dir returns no topics rather than failing. This lets the
// production binary boot even if the operator hasn't mounted the topics
// volume yet (k8s race during a rolling update).
func TestLoadTopicSources_D1_AbsentDirIsHandled(t *testing.T) {
	t.Parallel()
	src, err := loadTopicSources("/nonexistent/path/should/not/exist")
	if err != nil {
		t.Fatalf("loadTopicSources missing dir: %v (want nil)", err)
	}
	if len(src.Operator) != 0 {
		t.Errorf("operator topics: got %d, want 0", len(src.Operator))
	}
}

// TestLoadTopicSources_D1_LoadsJSONFiles asserts that each *.json file in
// the directory becomes one Operator-precedence RawTopic. This is the
// happy path — the matcher CatalogProvider now actually has content.
func TestLoadTopicSources_D1_LoadsJSONFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	body := `{"resourceType":"SubscriptionTopic","url":"http://x/y","version":"1","status":"active"}`
	for _, name := range []string{"a.json", "b.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Non-json file must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatalf("write README.txt: %v", err)
	}
	src, err := loadTopicSources(dir)
	if err != nil {
		t.Fatalf("loadTopicSources: %v", err)
	}
	if got := len(src.Operator); got != 2 {
		t.Errorf("operator topics: got %d, want 2", got)
	}
	for _, rt := range src.Operator {
		if rt.Origin == "" {
			t.Errorf("RawTopic.Origin must be non-empty for diagnostics")
		}
		if len(rt.Bytes) == 0 {
			t.Errorf("RawTopic.Bytes empty for origin %q", rt.Origin)
		}
	}
}

// TestLoadTopicSources_D1_RecursiveOff asserts that subdirectories are
// not walked recursively today; operators put topic files in a single
// flat directory (matches k8s ConfigMap mount semantics, where each key
// becomes a file at the volume root).
func TestLoadTopicSources_D1_RecursiveOff(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"resourceType":"SubscriptionTopic","url":"http://x/y","version":"1","status":"active"}`
	if err := os.WriteFile(filepath.Join(sub, "deep.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	src, err := loadTopicSources(dir)
	if err != nil {
		t.Fatalf("loadTopicSources: %v", err)
	}
	if got := len(src.Operator); got != 1 {
		t.Errorf("operator topics: got %d, want 1 (nested dir must be skipped)", got)
	}
}
