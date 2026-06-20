// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package lint hosts static-analysis gates that run as part of the
// regular Go test suite (no build tags), so they execute on every CI
// run without -tags e2e.
//
// The skip-gate (OP #259, H4 SkipScenarioGate) walks e2e/ source and
// refuses an unattributed t.Skip / t.Skipf call. A skip is attributed
// when an `OP #NNN` token appears either in the call's string args or
// in a `//` comment within three lines above the call. This keeps
// environmental short-circuit skips ("docker unavailable", "short
// mode") legitimate while requiring the author to point at the
// OpenProject ticket that explains why the skip exists.
package lint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var opCitation = regexp.MustCompile(`(?i)OP\s*#\s*\d+`)

// SkipFinding describes a single uncited t.Skip / t.Skipf call.
type SkipFinding struct {
	File string
	Line int
	Call string // "t.Skip" or "t.Skipf"
}

func (f SkipFinding) String() string {
	return fmt.Sprintf("%s:%d: %s call lacks an OP #NNN citation", f.File, f.Line, f.Call)
}

// FindUncitedSkips walks every *.go file under root, skipping any path
// whose cleaned form is listed in excludeDirs (or that lives beneath
// such a path), and returns every t.Skip / t.Skipf call that lacks an
// `OP #NNN` citation in its string args or in a preceding comment
// within three lines.
//
// Receiver name is not constrained: any *.Skip / *.Skipf selector is
// treated as a testing.T skip. The lint is intentionally conservative
// — false positives are silenced by adding a citation, and that is
// the explicit goal of OP #259.
func FindUncitedSkips(root string, excludeDirs []string) ([]SkipFinding, error) {
	cleanedRoot := filepath.Clean(root)
	excluded := make([]string, len(excludeDirs))
	for i, d := range excludeDirs {
		excluded[i] = filepath.Clean(d)
	}

	var findings []SkipFinding
	err := filepath.WalkDir(cleanedRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			for _, ex := range excluded {
				if path == ex {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fileFindings, err := findInFile(path)
		if err != nil {
			return err
		}
		findings = append(findings, fileFindings...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, nil
}

func findInFile(path string) ([]SkipFinding, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	commentLines := commentLineMap(fset, file.Comments)

	var findings []SkipFinding
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if name != "Skip" && name != "Skipf" {
			return true
		}
		if hasCitationInArgs(call.Args) {
			return true
		}
		callLine := fset.Position(call.Pos()).Line
		if hasCitationInPrecedingComments(commentLines, callLine) {
			return true
		}
		findings = append(findings, SkipFinding{
			File: path,
			Line: callLine,
			Call: "t." + name,
		})
		return true
	})
	return findings, nil
}

func hasCitationInArgs(args []ast.Expr) bool {
	for _, a := range args {
		lit, ok := a.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if opCitation.MatchString(lit.Value) {
			return true
		}
	}
	return false
}

// commentLineMap returns a map from source line -> comment text for
// every line covered by a comment in the file.
func commentLineMap(fset *token.FileSet, groups []*ast.CommentGroup) map[int]string {
	out := map[int]string{}
	for _, g := range groups {
		for _, c := range g.List {
			start := fset.Position(c.Pos()).Line
			end := fset.Position(c.End()).Line
			for line := start; line <= end; line++ {
				if existing, ok := out[line]; ok {
					out[line] = existing + "\n" + c.Text
				} else {
					out[line] = c.Text
				}
			}
		}
	}
	return out
}

func hasCitationInPrecedingComments(commentLines map[int]string, callLine int) bool {
	for offset := 1; offset <= 3; offset++ {
		line := callLine - offset
		if line < 1 {
			break
		}
		if text, ok := commentLines[line]; ok {
			if opCitation.MatchString(text) {
				return true
			}
		}
	}
	return false
}

// FormatFindings is a convenience for test output.
func FormatFindings(findings []SkipFinding) string {
	if len(findings) == 0 {
		return ""
	}
	parts := make([]string, len(findings))
	for i, f := range findings {
		parts[i] = "  " + f.String()
	}
	return strings.Join(parts, "\n")
}
