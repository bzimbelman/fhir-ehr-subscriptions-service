// Skip-age test. Drives a real `git init`/`git commit`/`git blame`
// cycle in a tempdir, with explicit author dates, so the F84
// detection runs against actual git history rather than a mock.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	staleSkipFile = "stale_skip_test.go"
	freshSkipFile = "fresh_skip_test.go"
)

const staleSkipBody = `package fixture

import "testing"

// TestStaleSkip's skip is intentionally old for the F84 fixture.
func TestStaleSkip(t *testing.T) {
	t.Skip("skipping — older than 30 days, must surface as F84")
}
`

const freshSkipBody = `package fixture

import "testing"

// TestFreshSkip's skip was just authored, so it must NOT surface.
func TestFreshSkip(t *testing.T) {
	t.Skip("just-added skip, should pass the age gate")
}
`

// initRepoWithDatedFile writes path with body, then commits it with
// the given author/committer date so `git blame --porcelain` reports
// that exact author-time.
func initRepoWithDatedFile(t *testing.T, root, relPath, body string, when time.Time) {
	t.Helper()
	full := filepath.Join(root, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	stamp := when.Format(time.RFC3339)
	cmd := exec.Command("git", "add", relPath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "fixture: "+relPath, "--date", stamp)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_DATE="+stamp,
		"GIT_COMMITTER_DATE="+stamp,
		"GIT_AUTHOR_NAME=Fixture Author",
		"GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture Author",
		"GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

func newSkipFixtureRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "fixture@example.com"},
		{"config", "user.name", "Fixture Author"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	now := time.Now().UTC()
	initRepoWithDatedFile(t, root, filepath.Join("internal", "feature", staleSkipFile),
		staleSkipBody, now.AddDate(0, 0, -45))
	initRepoWithDatedFile(t, root, filepath.Join("internal", "feature", freshSkipFile),
		freshSkipBody, now.AddDate(0, 0, -2))
	return root
}

// F84 — t.Skip authored more than 30 days ago must surface.
func TestRule_F84_FlagsStaleSkip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; F84 requires real git blame")
	}
	root := newSkipFixtureRepo(t)
	got, err := WalkSkipsForAge(root, []string{filepath.Join(root, "internal")}, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("WalkSkipsForAge: %v", err)
	}
	by := findingsByRule(got)
	f84 := by["F84"]
	if len(f84) != 1 {
		t.Fatalf("expected exactly 1 F84 finding (stale skip), got %d: %#v", len(f84), got)
	}
	if !strings.Contains(f84[0].File, staleSkipFile) {
		t.Errorf("F84 should fire on %q, fired on %q", staleSkipFile, f84[0].File)
	}
	if !strings.Contains(f84[0].Message, "30 days") {
		t.Errorf("F84 message should explain the 30-day rule, got: %q", f84[0].Message)
	}
}

// F84 — fresh skip MUST NOT fire.
func TestRule_F84_AllowsFreshSkip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; F84 requires real git blame")
	}
	root := newSkipFixtureRepo(t)
	got, err := WalkSkipsForAge(root, []string{filepath.Join(root, "internal")}, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("WalkSkipsForAge: %v", err)
	}
	for _, f := range got {
		if strings.Contains(f.File, freshSkipFile) {
			t.Errorf("F84 must not fire on a fresh (<30 day) skip, but did: %#v", f)
		}
	}
}
