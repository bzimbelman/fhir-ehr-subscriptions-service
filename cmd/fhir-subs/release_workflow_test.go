// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// findRepoRoot walks up from the test working directory until it finds
// the go.mod file that anchors this module. The release workflow lives
// at $REPO/.github/workflows/release.yml; the test is invariant to where
// `go test` is invoked from.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate repo root from %s", dir)
	return ""
}

// TestReleaseWorkflow_BinariesLdflagsUseCapitalizedVersion pins OP #209.
//
// release.yml drives the binary release matrix. The ldflag for embedding
// the build version MUST set main.Version (capital V) — the variable
// that actually exists in cmd/fhir-subs/version.go — not main.version.
// A lowercase reference is a no-op, so released binaries silently report
// version=dev.
//
// Acceptance criteria require the ldflag to set both main.Version
// (sourced from the git tag) and main.Commit (sourced from the SHA).
func TestReleaseWorkflow_BinariesLdflagsUseCapitalizedVersion(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "workflows", "release.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// Locate the build step's run script. We do not parse the whole
	// workflow YAML semantically; we only parse enough to pull the
	// matrix-build run block out so we can assert against the literal
	// ldflag string the runner executes.
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	binaries, ok := wf.Jobs["binaries"]
	if !ok {
		t.Fatalf("release.yml missing 'binaries' job")
	}

	var run string
	for _, s := range binaries.Steps {
		if s.Name == "build" {
			run = s.Run
			break
		}
	}
	if run == "" {
		t.Fatalf("release.yml binaries job is missing the 'build' step run script")
	}

	// The fix from #209: lowercase main.version was a no-op.
	if strings.Contains(run, "-X main.version=") {
		t.Errorf("release.yml uses lowercase -X main.version= (no-op); must be -X main.Version=:\n%s", run)
	}
	if !strings.Contains(run, "-X main.Version=") {
		t.Errorf("release.yml ldflags missing -X main.Version=:\n%s", run)
	}
	// AC also requires main.Commit so the commit field is no longer "dev".
	if !strings.Contains(run, "-X main.Commit=") {
		t.Errorf("release.yml ldflags missing -X main.Commit=:\n%s", run)
	}
}

// TestReleaseWorkflow_ImageBuildPassesVersionArgs pins OP #210.
//
// The docker build step in release.yml MUST pass VERSION and COMMIT as
// build-args to docker buildx so the image's go build picks them up
// via -ldflags. Without this, every image (CI, Helm, demo) reports
// version=dev even when the binary release reports the correct tag.
func TestReleaseWorkflow_ImageBuildPassesVersionArgs(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "workflows", "release.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	// docker/build-push-action accepts a `build-args:` key with a
	// multiline literal of `KEY=VALUE` pairs. Parse the YAML and walk
	// to that step.
	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string                 `yaml:"uses"`
				With map[string]interface{} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	imageJob, ok := wf.Jobs["image"]
	if !ok {
		t.Fatalf("release.yml missing 'image' job")
	}

	var args string
	for _, s := range imageJob.Steps {
		if !strings.HasPrefix(s.Uses, "docker/build-push-action@") {
			continue
		}
		if v, ok := s.With["build-args"]; ok {
			if as, ok := v.(string); ok {
				args = as
			}
		}
	}
	if args == "" {
		t.Fatalf("release.yml image job missing docker/build-push-action build-args")
	}
	if !strings.Contains(args, "VERSION=") {
		t.Errorf("release.yml image job build-args missing VERSION=: %q", args)
	}
	if !strings.Contains(args, "COMMIT=") {
		t.Errorf("release.yml image job build-args missing COMMIT=: %q", args)
	}
}
