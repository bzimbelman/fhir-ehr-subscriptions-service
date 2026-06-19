// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDockerfile_AcceptsVersionAndCommitBuildArgs pins OP #210.
//
// The container image must be reproducible from a (VERSION, COMMIT)
// pair. Today the Dockerfile silently bakes "dev" into the image
// because the go build step has no -ldflags '-X main.Version=...'.
// This test parses the Dockerfile and asserts the contract:
//
//   - ARG VERSION (declared so --build-arg VERSION=... is plumbed)
//   - ARG COMMIT  (likewise)
//   - the go build step passes -X main.Version=$VERSION and
//     -X main.Commit=$COMMIT in -ldflags
func TestDockerfile_AcceptsVersionAndCommitBuildArgs(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, "Dockerfile")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(body)

	if !regexp.MustCompile(`(?m)^\s*ARG\s+VERSION\b`).MatchString(src) {
		t.Errorf("Dockerfile missing 'ARG VERSION' (so --build-arg VERSION is honored)")
	}
	if !regexp.MustCompile(`(?m)^\s*ARG\s+COMMIT\b`).MatchString(src) {
		t.Errorf("Dockerfile missing 'ARG COMMIT' (so --build-arg COMMIT is honored)")
	}

	// Find the line with `go build` and confirm it passes ldflags
	// for main.Version and main.Commit. We don't lock the exact
	// surrounding flags (-trimpath, -s -w) — we only assert the two
	// load-bearing -X entries are present in some -ldflags="..." block
	// that lives in the same RUN as `go build`.
	if !strings.Contains(src, "go build") {
		t.Fatalf("Dockerfile lost its 'go build' step; the multi-stage build is broken")
	}

	// The ldflags block can span multiple shell-continued lines, so we
	// flatten the file into a single string before matching.
	flat := strings.ReplaceAll(src, "\\\n", " ")

	if !regexp.MustCompile(`-ldflags\s*=?\s*"[^"]*-X\s+main\.Version=\$\{?VERSION\}?`).MatchString(flat) {
		t.Errorf("Dockerfile go build is missing -ldflags '-X main.Version=$VERSION'\nflattened source:\n%s", flat)
	}
	if !regexp.MustCompile(`-ldflags\s*=?\s*"[^"]*-X\s+main\.Commit=\$\{?COMMIT\}?`).MatchString(flat) {
		t.Errorf("Dockerfile go build is missing -ldflags '-X main.Commit=$COMMIT'\nflattened source:\n%s", flat)
	}
}
