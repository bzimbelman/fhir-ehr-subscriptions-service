// prod-stub-lint entrypoint. Default invocation walks the repository
// containing the working directory's go.mod, scanning cmd/ and
// internal/, with a 30-day threshold for stale t.Skip calls. CI calls
// it as `go run ./tools/prod-stub-lint` and treats a non-zero exit as
// a build failure.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	var (
		repoFlag    = flag.String("repo", "", "repository root (defaults to walking up to go.mod from cwd)")
		rootsFlag   = flag.String("roots", "cmd,internal", "comma-separated list of production roots to scan (relative to repo)")
		skipMaxDays = flag.Int("skip-max-days", 30, "maximum age, in days, for any t.Skip in production tests")
		failOnStubs = flag.Bool("fail-on-stubs", true, "exit non-zero if any rule fires (default: true). Set false to print findings without failing.")
	)
	flag.Parse()

	repo, err := resolveRepoRoot(*repoFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prod-stub-lint: %v\n", err)
		os.Exit(2)
	}
	roots := splitRoots(repo, *rootsFlag)

	astFindings, err := WalkProductionTrees(roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prod-stub-lint: AST walk failed: %v\n", err)
		os.Exit(2)
	}
	skipFindings, err := WalkSkipsForAge(repo, roots, time.Duration(*skipMaxDays)*24*time.Hour)
	if err != nil {
		fmt.Fprintf(os.Stderr, "prod-stub-lint: skip-age walk failed: %v\n", err)
		os.Exit(2)
	}
	all := make([]Finding, 0, len(astFindings)+len(skipFindings))
	all = append(all, astFindings...)
	all = append(all, skipFindings...)
	for _, f := range all {
		fmt.Printf("%s:%d: [%s] %s\n", f.File, f.Line, f.Rule, f.Message)
	}
	fmt.Printf("\nprod-stub-lint: %d finding(s) across %d root(s) under %s\n", len(all), len(roots), repo)
	if len(all) > 0 && *failOnStubs {
		os.Exit(1)
	}
}

func splitRoots(repo, csv string) []string {
	var out []string
	for _, raw := range filepath.SplitList(csv) {
		out = append(out, raw)
	}
	if len(out) == 1 && csv != "" {
		out = nil
		for _, raw := range splitOnComma(csv) {
			out = append(out, filepath.Join(repo, raw))
		}
		return out
	}
	out = nil
	for _, raw := range splitOnComma(csv) {
		out = append(out, filepath.Join(repo, raw))
	}
	return out
}

func splitOnComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// resolveRepoRoot finds the repository root by walking up from the
// supplied path (or cwd) until a go.mod file is found.
func resolveRepoRoot(explicit string) (string, error) {
	start := explicit
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("getwd: %w", err)
		}
		start = cwd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("abs %s: %w", start, err)
	}
	for cur := abs; ; {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no go.mod found at or above %s", abs)
		}
		cur = parent
	}
}
