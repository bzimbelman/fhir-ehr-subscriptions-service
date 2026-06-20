// Skip-age walker for prod-stub-lint. For each *_test.go file under
// the supplied roots, it locates every t.Skip / t.Skipf / t.SkipNow
// call site, then runs `git blame --porcelain -L <line>,<line>` to
// learn the author-time of that exact line. If the author-time is
// older than the configured threshold the call is reported as F84.
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// WalkSkipsForAge inspects every *_test.go file under each root and
// returns F84 findings for t.Skip-family calls whose blame
// author-time is older than threshold.
func WalkSkipsForAge(repoRoot string, roots []string, threshold time.Duration) ([]Finding, error) {
	var findings []Finding
	fset := token.NewFileSet()
	now := time.Now().UTC()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return fmt.Errorf("parse %s: %w", path, perr)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if !isSkipCall(call) {
					return true
				}
				line := fset.Position(call.Pos()).Line
				when, err := blameLineDate(repoRoot, path, line)
				if err != nil {
					findings = append(findings, Finding{
						Rule:    "F84",
						File:    path,
						Line:    line,
						Message: fmt.Sprintf("t.Skip blame lookup failed (%v); cannot prove the skip is fresh, treat as stale", err),
					})
					return true
				}
				if now.Sub(when) > threshold {
					findings = append(findings, Finding{
						Rule:    "F84",
						File:    path,
						Line:    line,
						Message: fmt.Sprintf("t.Skip authored %s (%.0f days ago) is older than the 30 days policy; replace the skip with a real check or delete the test", when.Format("2006-01-02"), now.Sub(when).Hours()/24),
					})
				}
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return findings, nil
}

// isSkipCall returns true for t.Skip, t.Skipf, or t.SkipNow — the
// three skip APIs the testing package exposes.
func isSkipCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Skip", "Skipf", "SkipNow":
		return true
	}
	return false
}

// blameLineDate runs `git blame --porcelain -L line,line` against
// path under repoRoot and returns the author-time as a UTC time.Time.
func blameLineDate(repoRoot, path string, line int) (time.Time, error) {
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return time.Time{}, fmt.Errorf("rel %s under %s: %w", path, repoRoot, err)
	}
	//nolint:gosec // inputs to `git blame` are repo-internal: rel comes from filepath.WalkDir under repoRoot and line is the AST position of a *.Skip call site, both controlled by this tool
	cmd := exec.Command("git", "blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", line, line), "--", rel)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return time.Time{}, fmt.Errorf("git blame: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	scanner := bufio.NewScanner(&stdout)
	for scanner.Scan() {
		raw := scanner.Text()
		if !strings.HasPrefix(raw, "author-time ") {
			continue
		}
		secs, perr := strconv.ParseInt(strings.TrimPrefix(raw, "author-time "), 10, 64)
		if perr != nil {
			return time.Time{}, fmt.Errorf("parse author-time %q: %w", raw, perr)
		}
		return time.Unix(secs, 0).UTC(), nil
	}
	if err := scanner.Err(); err != nil {
		return time.Time{}, err
	}
	return time.Time{}, fmt.Errorf("no author-time line in `git blame` output for %s:%d", path, line)
}
