// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package orchestrator

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// runCheckConfig builds the production binary, writes the supplied
// YAML to a temp file, and exec's `fhir-subs --config <file>
// --check-config`. Returns (exitCode, stdout, stderr).
//
// The build is shared across all callers via prodBinaryPath so the
// e2e suite doesn't pay a `go build` cost on every check-config test.
func runCheckConfig(t *testing.T, yamlBody string) (int, string, string) {
	t.Helper()

	binPath := prodBinaryPathOnce(t)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(yamlBody), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	cmd := exec.Command(binPath, "--config", cfgPath, "--check-config") //nolint:gosec // operator binary path is the test artifact
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	rc := 0
	if err != nil {
		var exitErr *exec.ExitError
		if asErr, ok := err.(*exec.ExitError); ok {
			exitErr = asErr
		} else {
			t.Fatalf("run check-config: %v", err)
		}
		rc = exitErr.ExitCode()
	}
	return rc, stdout.String(), stderr.String()
}

var (
	prodBinaryBuildOnce sync.Once
	prodBinaryBuiltPath string
	prodBinaryBuildErr  error
)

// prodBinaryPathOnce builds the production binary once per test
// process and returns the absolute path. Concurrent callers share the
// single build artifact.
func prodBinaryPathOnce(t *testing.T) string {
	t.Helper()
	prodBinaryBuildOnce.Do(func() {
		repoRoot, err := findRepoRoot()
		if err != nil {
			prodBinaryBuildErr = err
			return
		}
		dir, err := os.MkdirTemp("", "fhir-subs-bin-*")
		if err != nil {
			prodBinaryBuildErr = err
			return
		}
		out := filepath.Join(dir, "fhir-subs")
		build := exec.Command("go", "build", "-o", out, "./cmd/fhir-subs")
		build.Dir = repoRoot
		if combined, berr := build.CombinedOutput(); berr != nil {
			prodBinaryBuildErr = berr
			t.Logf("go build output:\n%s", combined)
			return
		}
		prodBinaryBuiltPath = out
	})
	if prodBinaryBuildErr != nil {
		t.Fatalf("build production binary: %v", prodBinaryBuildErr)
	}
	return prodBinaryBuiltPath
}
