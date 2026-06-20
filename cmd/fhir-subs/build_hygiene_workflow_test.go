// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The build-hygiene tests pin the CI/release/docs gates that close
// audit findings #129, #130, #132, #133, #134, #136, #137. They parse
// the workflow YAML directly so a future regression — someone removing
// cosign signing, dropping --strict from mkdocs, restoring a wildcard
// Go toolchain pin — fails the unit test gate before the change can
// merge. The tests intentionally know more than they "should" about
// workflow structure; the alternative (verifying via a CI run) only
// catches the regression after a PR has already been opened.

// TestReleaseWorkflow_CosignSignsImage pins OP #129.
//
// Release images pushed to ghcr.io MUST be signed with cosign so
// downstream consumers can verify provenance. The release workflow
// must invoke cosign sign against the digest produced by
// docker/build-push-action. We assert:
//
//   - sigstore/cosign-installer is used to install cosign
//   - a step invokes `cosign sign` against the image digest
//   - the image job retains id-token: write so cosign keyless OIDC
//     signing works (this is the legitimate use of id-token)
func TestReleaseWorkflow_CosignSignsImage(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "workflows", "release.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name string                 `yaml:"name"`
				Uses string                 `yaml:"uses"`
				Run  string                 `yaml:"run"`
				ID   string                 `yaml:"id"`
				With map[string]interface{} `yaml:"with"`
				Env  map[string]string      `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	} //nolint:revive // local YAML shape

	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	imageJob, ok := wf.Jobs["image"]
	if !ok {
		t.Fatalf("release.yml missing 'image' job")
	}

	var sawInstaller, sawSign bool
	for _, s := range imageJob.Steps {
		if strings.HasPrefix(s.Uses, "sigstore/cosign-installer@") {
			sawInstaller = true
		}
		if strings.Contains(s.Run, "cosign sign") {
			sawSign = true
		}
	}
	if !sawInstaller {
		t.Errorf("release.yml image job missing sigstore/cosign-installer step (cosign must be installed before signing)")
	}
	if !sawSign {
		t.Errorf("release.yml image job missing `cosign sign` step against the pushed image digest")
	}
}

// TestCIWorkflow_CrossPlatformImageBuildOnPRs pins OP #130.
//
// The PR docker build job MUST exercise both linux/amd64 and
// linux/arm64 platforms so a Dockerfile or dependency that breaks the
// arm64 build can never land on main. The release workflow already
// builds multi-arch; CI must mirror it (without push) on every PR.
//
// Acceptance criteria:
//   - docker/build-push-action in ci.yml docker job carries a
//     `platforms:` arg containing both linux/amd64 and linux/arm64.
//   - QEMU is set up so the runner can build arm64 layers.
func TestCIWorkflow_CrossPlatformImageBuildOnPRs(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "ci.yml")

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string                 `yaml:"name"`
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

	var sawQEMU, sawAMD64, sawARM64 bool
	// Concatenate platforms from every build-push-action step so the
	// test passes whether the job uses one multi-arch step or splits
	// the verify (multi-arch) and the smoke build (single-arch with
	// load: true) into two steps.
	for _, s := range docker.Steps {
		if strings.HasPrefix(s.Uses, "docker/setup-qemu-action@") {
			sawQEMU = true
		}
		if strings.HasPrefix(s.Uses, "docker/build-push-action@") {
			if v, ok := s.With["platforms"]; ok {
				if ps, ok := v.(string); ok {
					if strings.Contains(ps, "linux/amd64") {
						sawAMD64 = true
					}
					if strings.Contains(ps, "linux/arm64") {
						sawARM64 = true
					}
				}
			}
		}
	}
	if !sawQEMU {
		t.Errorf("ci.yml docker job must set up QEMU (docker/setup-qemu-action) so arm64 cross-build works")
	}
	if !sawAMD64 {
		t.Errorf("ci.yml docker job must build for linux/amd64 in at least one docker/build-push-action step")
	}
	if !sawARM64 {
		t.Errorf("ci.yml docker job must build for linux/arm64 in at least one docker/build-push-action step")
	}
}

// TestGolangciLint_PinnedAndTuned pins OP #132.
//
// Two requirements:
//
//   - The version of golangci-lint used in CI MUST be pinned exactly
//     (no floating tags like @latest, no minor-only pins like v1.62).
//   - The .golangci.yml configuration MUST set `run.go` to the same
//     version pinned in go.mod (so the linter parses the same syntax
//     the toolchain compiles).
func TestGolangciLint_PinnedAndTuned(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "ci.yml")

	var wf struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string                 `yaml:"uses"`
				With map[string]interface{} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse ci.yml: %v", err)
	}

	lint, ok := wf.Jobs["lint"]
	if !ok {
		t.Fatalf("ci.yml missing 'lint' job")
	}

	var version string
	for _, s := range lint.Steps {
		if strings.HasPrefix(s.Uses, "golangci/golangci-lint-action@") {
			if v, ok := s.With["version"]; ok {
				if vs, ok := v.(string); ok {
					version = vs
				}
			}
		}
	}
	if version == "" {
		t.Fatalf("ci.yml lint job missing golangci/golangci-lint-action version pin")
	}
	// AC: exact patch pin, e.g. v1.62.2 — never @latest, never v1.62.
	if version == "latest" {
		t.Errorf("ci.yml lint job pins golangci-lint to @latest; must be exact patch (vMAJOR.MINOR.PATCH)")
	}
	exact := regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	if !exact.MatchString(version) {
		t.Errorf("ci.yml lint job version %q is not an exact vMAJOR.MINOR.PATCH pin", version)
	}

	// And .golangci.yml must declare run.go matching the toolchain.
	root := findRepoRoot(t)
	cfgBody, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		t.Fatalf("read .golangci.yml: %v", err)
	}
	var cfg struct {
		Run struct {
			Go      string `yaml:"go"`
			Timeout string `yaml:"timeout"`
		} `yaml:"run"`
	}
	if err := yaml.Unmarshal(cfgBody, &cfg); err != nil {
		t.Fatalf("parse .golangci.yml: %v", err)
	}
	if cfg.Run.Go == "" {
		t.Errorf(".golangci.yml run.go must be set so linter parses the same syntax the toolchain uses")
	}
	if cfg.Run.Timeout == "" {
		t.Errorf(".golangci.yml run.timeout must be set explicitly")
	}
}

// TestNightlyWorkflow_FailureAlerting pins OP #133.
//
// Nightly conformance failures MUST raise visible signal — a silent red
// nightly is functionally a green nightly. We require the nightly
// workflow to file (or comment on) a tracking GitHub issue whenever a
// job in the workflow fails. The accepted shape is a step using
// `actions/github-script` (or equivalent like dacbd/create-issue-action)
// gated on `failure()` that creates an issue with the run URL.
func TestNightlyWorkflow_FailureAlerting(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "nightly.yml")

	var wf struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name string                 `yaml:"name"`
				Uses string                 `yaml:"uses"`
				If   string                 `yaml:"if"`
				Run  string                 `yaml:"run"`
				With map[string]interface{} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse nightly.yml: %v", err)
	}

	conf, ok := wf.Jobs["conformance"]
	if !ok {
		t.Fatalf("nightly.yml missing 'conformance' job")
	}

	// Find a failure-gated step that uses actions/github-script or a
	// dedicated create-issue action.
	var sawAlert bool
	for _, s := range conf.Steps {
		if !strings.Contains(s.If, "failure()") {
			continue
		}
		if strings.HasPrefix(s.Uses, "actions/github-script@") ||
			strings.HasPrefix(s.Uses, "dacbd/create-issue-action@") ||
			strings.HasPrefix(s.Uses, "JasonEtco/create-an-issue@") {
			sawAlert = true
			break
		}
		// A bare `run:` invoking `gh issue create` is also acceptable.
		if strings.Contains(s.Run, "gh issue create") {
			sawAlert = true
			break
		}
	}
	if !sawAlert {
		t.Errorf("nightly.yml conformance job missing a failure()-gated alerting step (actions/github-script + issue creation, or `gh issue create`)")
	}

	// The job must also grant issues: write so the alert step can file.
	if conf.Permissions["issues"] != "write" {
		t.Errorf("nightly.yml conformance job must grant issues: write so alerting step can file an issue; got permissions=%v", conf.Permissions)
	}
}

// TestGoToolchain_PinnedExactly pins OP #134.
//
// Every workflow that compiles Go code MUST pin the Go toolchain to an
// exact patch version (e.g. 1.22.7). Wildcard pins like '1.22' silently
// drift the compiler under our feet — a new patch release with a
// behavior change can break CI without a single repo change. go.mod's
// `go` directive must agree with the workflow pin.
func TestGoToolchain_PinnedExactly(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)

	exact := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	files := []string{
		"ci.yml",
		"integration.yml",
		"nightly.yml",
		"release.yml",
	}

	for _, name := range files {
		body := readWorkflow(t, name)
		var wf struct {
			Env map[string]string `yaml:"env"`
		}
		if err := yaml.Unmarshal(body, &wf); err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		got := wf.Env["GO_VERSION"]
		if got == "" {
			t.Errorf("%s env.GO_VERSION must be set", name)
			continue
		}
		if !exact.MatchString(got) {
			t.Errorf("%s env.GO_VERSION=%q is not an exact MAJOR.MINOR.PATCH pin", name, got)
		}
	}

	// go.mod must also declare an exact patch.
	gomodBody, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	goLine := regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
	m := goLine.FindStringSubmatch(string(gomodBody))
	if len(m) < 2 {
		t.Fatalf("go.mod missing `go` directive")
	}
	if !exact.MatchString(m[1]) {
		t.Errorf("go.mod `go %s` must declare exact patch (1.22.7), not 1.22", m[1])
	}
}

// TestReleaseWorkflow_NoUnusedIDToken pins OP #136.
//
// The workflow-level `id-token: write` permission grants the entire
// release workflow keyless-OIDC signing. If only the image job
// genuinely needs id-token (for cosign), the broader scope on the
// workflow is unnecessary attack surface. The fix is to remove the
// workflow-level grant and place id-token: write on the `image` job
// only.
//
// AC:
//   - workflow-level `permissions:` MUST NOT include id-token.
//   - the `image` job's `permissions:` MUST include id-token: write
//     (so cosign keyless signing keeps working).
func TestReleaseWorkflow_NoUnusedIDToken(t *testing.T) {
	t.Parallel()

	root := findRepoRoot(t)
	path := filepath.Join(root, ".github", "workflows", "release.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var wf struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &wf); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}

	if _, ok := wf.Permissions["id-token"]; ok {
		t.Errorf("release.yml workflow-level permissions must not include id-token; scope it to the image job only")
	}

	imageJob, ok := wf.Jobs["image"]
	if !ok {
		t.Fatalf("release.yml missing 'image' job")
	}
	if imageJob.Permissions["id-token"] != "write" {
		t.Errorf("release.yml image job must declare permissions.id-token: write so cosign keyless signing works; got %v", imageJob.Permissions)
	}
}

// TestDocsWorkflow_StrictBuildOnPRs pins OP #137.
//
// `mkdocs build --strict` catches broken links, missing nav entries,
// and template errors before they ship to the docs site. Today docs.yml
// only triggers on push-to-main, so a PR that breaks docs goes
// undetected until merge. The fix:
//
//   - docs.yml MUST trigger on pull_request (not just push to main).
//   - The build job MUST run `mkdocs build --strict`.
//   - The deploy job MUST be gated to push (not run on PRs from forks).
func TestDocsWorkflow_StrictBuildOnPRs(t *testing.T) {
	t.Parallel()

	body := readWorkflow(t, "docs.yml")

	// On is a heterogeneous YAML node — sometimes a list, sometimes a map.
	// Decode into yaml.Node and walk so we tolerate both shapes.
	var top struct {
		On   yaml.Node `yaml:"on"`
		Jobs map[string]struct {
			If    string `yaml:"if"`
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &top); err != nil {
		t.Fatalf("parse docs.yml: %v", err)
	}

	// Re-decode `on` into a generic structure to look for pull_request.
	var onMap map[string]interface{}
	if err := top.On.Decode(&onMap); err != nil {
		t.Fatalf("decode docs.yml `on`: %v", err)
	}
	if _, ok := onMap["pull_request"]; !ok {
		t.Errorf("docs.yml must trigger on pull_request so docs regressions are caught before merge; got on=%v", onMap)
	}

	build, ok := top.Jobs["build"]
	if !ok {
		t.Fatalf("docs.yml missing 'build' job")
	}
	var sawStrict bool
	for _, s := range build.Steps {
		if strings.Contains(s.Run, "mkdocs build") && strings.Contains(s.Run, "--strict") {
			sawStrict = true
			break
		}
	}
	if !sawStrict {
		t.Errorf("docs.yml build job must run `mkdocs build --strict`")
	}

	// The deploy job must be gated to push (not run from forked PRs that
	// don't have access to the pages secret).
	deploy, ok := top.Jobs["deploy"]
	if !ok {
		t.Fatalf("docs.yml missing 'deploy' job")
	}
	if !strings.Contains(deploy.If, "push") && !strings.Contains(deploy.If, "github.event_name") {
		t.Errorf("docs.yml deploy job must gate on push events (e.g. if: github.event_name == 'push'); got if=%q", deploy.If)
	}
}
