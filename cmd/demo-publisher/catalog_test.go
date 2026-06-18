// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// openProjectFile resolves a repo-relative path to an absolute path and
// opens it. Used so the demo catalog test isn't sensitive to where `go
// test` is invoked from.
func openProjectFile(t *testing.T, rel string) (*os.File, error) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return os.Open(filepath.Join(repoRoot, rel))
}

func TestLoadCatalog_ParsesYAMLMessages(t *testing.T) {
	t.Parallel()
	yamlIn := `messages:
  - description: "Lab result for ABC123"
    delay: 1s
    template: oru-r01
    fields:
      patient_id: "ABC123"
      observation_code: "718-7"
      value: "13.5"
      unit: "g/dL"
  - description: "Encounter admit for ABC123"
    delay: 500ms
    template: adt-a01
    fields:
      patient_id: "ABC123"
      facility: "Demo Hospital"
`
	cat, err := loadCatalog(strings.NewReader(yamlIn))
	if err != nil {
		t.Fatalf("loadCatalog: %v", err)
	}
	if len(cat.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(cat.Messages))
	}
	first := cat.Messages[0]
	if first.Description != "Lab result for ABC123" {
		t.Fatalf("description: got %q", first.Description)
	}
	if first.Delay != time.Second {
		t.Fatalf("delay: got %v, want 1s", first.Delay)
	}
	if first.Template != "oru-r01" {
		t.Fatalf("template: got %q", first.Template)
	}
	if first.Fields["patient_id"] != "ABC123" {
		t.Fatalf("patient_id: got %q", first.Fields["patient_id"])
	}
	if first.Fields["observation_code"] != "718-7" {
		t.Fatalf("observation_code: got %q", first.Fields["observation_code"])
	}
	if cat.Messages[1].Delay != 500*time.Millisecond {
		t.Fatalf("second delay: got %v", cat.Messages[1].Delay)
	}
}

func TestLoadCatalog_RejectsUnknownTemplate(t *testing.T) {
	t.Parallel()
	yamlIn := `messages:
  - description: "bad"
    template: nope-bogus
    fields:
      patient_id: "X"
`
	_, err := loadCatalog(strings.NewReader(yamlIn))
	if err == nil {
		t.Fatal("expected error for unknown template, got nil")
	}
	if !strings.Contains(err.Error(), "nope-bogus") {
		t.Fatalf("expected error to mention template name; got: %v", err)
	}
}

func TestLoadCatalog_RejectsEmpty(t *testing.T) {
	t.Parallel()
	_, err := loadCatalog(strings.NewReader("messages: []\n"))
	if err == nil {
		t.Fatal("expected error for empty catalog, got nil")
	}
}

func TestLoadCatalog_DemoLabsYAMLLoads(t *testing.T) {
	t.Parallel()
	// The shipped demo catalog must always parse — it's the file we point
	// users at in the demo doc and in --help output.
	f, err := openProjectFile(t, "demo/scenarios/labs.yaml")
	if err != nil {
		t.Fatalf("open demo catalog: %v", err)
	}
	defer f.Close()
	cat, err := loadCatalog(f)
	if err != nil {
		t.Fatalf("loadCatalog(demo/scenarios/labs.yaml): %v", err)
	}
	if len(cat.Messages) < 2 {
		t.Fatalf("demo catalog should have at least 2 messages, got %d", len(cat.Messages))
	}
}

func TestLoadCatalog_RejectsMissingPatientID(t *testing.T) {
	t.Parallel()
	// oru-r01 requires patient_id
	yamlIn := `messages:
  - description: "missing patient"
    template: oru-r01
    fields:
      observation_code: "718-7"
      value: "13.5"
`
	_, err := loadCatalog(strings.NewReader(yamlIn))
	if err == nil {
		t.Fatal("expected error for missing patient_id, got nil")
	}
	if !strings.Contains(err.Error(), "patient_id") {
		t.Fatalf("expected error to mention patient_id; got: %v", err)
	}
}
