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

// The ci_workflow tests pin the CI gates required by the
// production-readiness audit (#63, #64, #65, #68, #72) into the
// .github/ tree. They parse the workflow YAML directly so a future
// regression — someone deleting a job, restoring a label gate, or
// dropping the coverage step — fails the unit test gate. The tests
// intentionally know more than they "should" about workflow structure;
// the alternative (verifying via a CI run) only catches the regression
// after a PR has already been opened against main.

// readWorkflow loads .github/workflows/<name> from the repo root.
func readWorkflow(t *testing.T, name string) []byte {
	t.Helper()
	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "workflows", name)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return body
}

// TestIntegrationWorkflow_AlwaysRunsOnPRs pins OP #126.
//
// Default PRs MUST run integration tests; the previous
// `run-integration` label gate let audit-schema regressions, partition
// rotation regressions, and empty-catalog dead-pipeline bugs ship on
// "green" CI. The label gate is removed; the integration job's `if:`
// clause must allow every pull_request event without depending on a
// label.
func TestIntegrationWorkflow_AlwaysRunsOnPRs(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "integration.yml")

	var wf struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse integration.yml: %v", err)
	}

	intg, ok := wf.Jobs["integration"]
	if !ok {
		t.Fatalf("integration.yml missing 'integration' job")
	}

	// AC: drop the `run-integration` label gate. Either the `if:` is
	// absent, or it explicitly does NOT depend on the label.
	if strings.Contains(intg.If, "run-integration") {
		t.Errorf("integration.yml integration job still gates on run-integration label:\n%s", intg.If)
	}
	// Also assert the integration test step is still present and runs
	// the integration build tag.
	var foundStep bool
	for _, s := range intg.Steps {
		if strings.Contains(s.Run, "-tags integration") {
			foundStep = true
			break
		}
	}
	if !foundStep {
		t.Errorf("integration.yml integration job missing `-tags integration` test step")
	}
}

// TestE2EWorkflow_SmokeAlwaysRunsOnPRs pins OP #127.
//
// The default-PR e2e gate MUST run the smoke subset on every PR. The
// full suite remains gated to `main` and the `full-e2e` label so PRs
// stay fast. The previous label-only gate left default PRs with zero
// e2e coverage.
//
// Acceptance criteria:
//   - Drop the e2e label gate so smoke runs on every PR.
//   - Smoke subset (~3 min) on every PR.
//   - Full suite on `main` and on PRs labeled `full-e2e`.
//   - CI MUST fail on e2e failure.
func TestE2EWorkflow_SmokeAlwaysRunsOnPRs(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "integration.yml")

	var wf struct {
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse integration.yml: %v", err)
	}

	smoke, ok := wf.Jobs["e2e-smoke"]
	if !ok {
		t.Fatalf("integration.yml missing 'e2e-smoke' job (smoke subset must run on every PR)")
	}
	// Smoke must NOT gate on a label.
	if strings.Contains(smoke.If, "labels") || strings.Contains(smoke.If, "run-e2e") || strings.Contains(smoke.If, "run-integration") {
		t.Errorf("e2e-smoke job must run on every PR, not gate on labels:\n%s", smoke.If)
	}
	// Smoke must execute go test against the smoke subset.
	var smokeRuns bool
	for _, s := range smoke.Steps {
		if strings.Contains(s.Run, "-tags e2e_smoke") || strings.Contains(s.Run, "e2e/smoke") {
			smokeRuns = true
			break
		}
	}
	if !smokeRuns {
		t.Errorf("e2e-smoke job missing the `-tags e2e_smoke` test step or e2e/smoke target")
	}

	// The full e2e job must still exist for main + full-e2e label.
	full, ok := wf.Jobs["e2e"]
	if !ok {
		t.Fatalf("integration.yml missing 'e2e' job (full e2e suite for main + full-e2e label)")
	}
	if !strings.Contains(full.If, "full-e2e") && !strings.Contains(full.If, "github.event_name == 'push'") {
		t.Errorf("e2e (full) job must allow main pushes and full-e2e label PRs:\n%s", full.If)
	}
}

// TestCIWorkflow_EnforcesCoverageThreshold pins OP #128.
//
// CI MUST parse cover.out and fail the build below the threshold from
// .coverage-thresholds.json (default 80%). The previous behavior was
// "produce coverage artifact, never enforce" — the project docs claim
// 80% but no gate ever fires.
//
// Acceptance criteria:
//   - Add a step that parses cover.out and fails the build below
//     `.coverage-thresholds.json` (default 80%).
//   - CI failure MUST link to the offending packages.
func TestCIWorkflow_EnforcesCoverageThreshold(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "ci.yml")

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}

	test, ok := wf.Jobs["test"]
	if !ok {
		t.Fatalf("ci.yml missing 'test' job")
	}

	var foundCoverGate bool
	for _, s := range test.Steps {
		// The gate is a script that reads the threshold file and the
		// coverage profile and exits non-zero on shortfall.
		if strings.Contains(s.Run, "coverage-thresholds.json") {
			foundCoverGate = true
			break
		}
	}
	if !foundCoverGate {
		t.Errorf("ci.yml test job missing coverage-threshold enforcement step (expected reference to .coverage-thresholds.json)")
	}

	// AC also requires the threshold file itself to exist at repo root.
	root := findRepoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".coverage-thresholds.json")); err != nil {
		t.Errorf(".coverage-thresholds.json missing at repo root: %v", err)
	}
}

// TestCIWorkflow_RunsGovulncheck pins OP #131.
//
// CI MUST run `govulncheck ./...` on every PR/push. CodeQL is
// SAST-only; without govulncheck the dep graph can ship a known CVE
// and CI stays green. Failure MUST block merge.
func TestCIWorkflow_RunsGovulncheck(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "ci.yml")

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
				Uses string `yaml:"uses"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}

	vuln, ok := wf.Jobs["govulncheck"]
	if !ok {
		t.Fatalf("ci.yml missing 'govulncheck' job")
	}

	// At least one step must invoke govulncheck against ./...
	var ran bool
	for _, s := range vuln.Steps {
		if strings.Contains(s.Run, "govulncheck") && strings.Contains(s.Run, "./...") {
			ran = true
			break
		}
		// The official action is also acceptable.
		if strings.HasPrefix(s.Uses, "golang/govulncheck-action@") {
			ran = true
			break
		}
	}
	if !ran {
		t.Errorf("ci.yml govulncheck job must run `govulncheck ./...` (or use golang/govulncheck-action)")
	}
}

// TestCIWorkflow_DependabotConfigured pins OP #131 part B.
//
// .github/dependabot.yml MUST exist and cover Go modules, GitHub
// Actions, and Docker base images so dep updates land as PRs.
func TestCIWorkflow_DependabotConfigured(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "dependabot.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read .github/dependabot.yml: %v", err)
	}

	var cfg struct {
		Version int `yaml:"version"`
		Updates []struct {
			PackageEcosystem string `yaml:"package-ecosystem"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse dependabot.yml: %v", err)
	}
	if cfg.Version != 2 {
		t.Errorf("dependabot.yml version must be 2, got %d", cfg.Version)
	}

	want := map[string]bool{
		"gomod":          false,
		"github-actions": false,
		"docker":         false,
	}
	for _, u := range cfg.Updates {
		if _, ok := want[u.PackageEcosystem]; ok {
			want[u.PackageEcosystem] = true
		}
	}
	for eco, found := range want {
		if !found {
			t.Errorf("dependabot.yml missing package-ecosystem: %q", eco)
		}
	}
}

// TestCIWorkflow_SmokeRunsBuiltImage pins OP #135.
//
// The docker job builds the image; CI MUST then exercise the binary
// inside that image so a regression in entrypoint, ldflag wiring, or
// probe surface fails the gate. We assert three runs:
//
//   - `--check-config <fixture>.yaml` against a probe-only fixture, exit 0
//   - `--version` produces a non-empty banner (depends on ldflag fix)
//   - 10-second probe-only smoke that hits /healthz
//
// The image build step itself must keep `load: true` so the run steps
// see the local image.
func TestCIWorkflow_SmokeRunsBuiltImage(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "ci.yml")

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string                 `yaml:"name"`
				Run  string                 `yaml:"run"`
				Uses string                 `yaml:"uses"`
				With map[string]interface{} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}

	docker, ok := wf.Jobs["docker"]
	if !ok {
		t.Fatalf("ci.yml missing 'docker' job")
	}

	// load: true so subsequent docker run steps see the image.
	var hasLoad bool
	for _, s := range docker.Steps {
		if !strings.HasPrefix(s.Uses, "docker/build-push-action@") {
			continue
		}
		if v, ok := s.With["load"]; ok {
			switch vv := v.(type) {
			case bool:
				hasLoad = vv
			case string:
				hasLoad = vv == "true"
			}
		}
	}
	if !hasLoad {
		t.Errorf("ci.yml docker job must set load: true so smoke steps can run the built image")
	}

	// Walk run steps for the three required smoke invocations.
	var sawVersion, sawCheckConfig, sawHealthz bool
	for _, s := range docker.Steps {
		run := s.Run
		if strings.Contains(run, "docker run") && strings.Contains(run, "--version") {
			sawVersion = true
		}
		if strings.Contains(run, "docker run") && strings.Contains(run, "--check-config") {
			sawCheckConfig = true
		}
		// healthz smoke: a docker run that probes /healthz.
		if strings.Contains(run, "/healthz") {
			sawHealthz = true
		}
	}
	if !sawVersion {
		t.Errorf("ci.yml docker job missing `docker run ... --version` smoke step")
	}
	if !sawCheckConfig {
		t.Errorf("ci.yml docker job missing `docker run ... --check-config` smoke step")
	}
	if !sawHealthz {
		t.Errorf("ci.yml docker job missing /healthz probe smoke step")
	}
}
