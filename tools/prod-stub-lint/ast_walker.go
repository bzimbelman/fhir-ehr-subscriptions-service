// AST walker for prod-stub-lint. Implements F49 (interface-nil
// placeholder), F50 (forbidden stub identifiers), and F119 (e2e/
// imports from production code) by parsing every non-test .go file
// under the supplied production roots with go/parser and walking the
// resulting *ast.File with go/ast.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Finding is a single lint violation. It captures enough context for a
// developer to navigate to the offending source line.
type Finding struct {
	Rule    string
	File    string
	Line    int
	Message string
}

// forbiddenStubIdents is the closed list of test-time stub identifiers
// the binary must never carry into production. Maintained alongside
// the team-lead message and OpenProject #264 acceptance criteria.
var forbiddenStubIdents = []string{
	"defaultActivator",
	"noopReplayer",
	"stubChannelActivator",
}

// WalkProductionTrees parses every non-test .go file under each root
// and returns findings for any rule violations discovered.
func WalkProductionTrees(roots []string) ([]Finding, error) {
	var findings []Finding
	fset := token.NewFileSet()
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if perr != nil {
				return fmt.Errorf("parse %s: %w", path, perr)
			}
			findings = append(findings, lintFile(fset, path, file)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		if findings[i].Line != findings[j].Line {
			return findings[i].Line < findings[j].Line
		}
		return findings[i].Rule < findings[j].Rule
	})
	return findings, nil
}

func lintFile(fset *token.FileSet, path string, file *ast.File) []Finding {
	var out []Finding
	out = append(out, checkE2EImports(fset, path, file)...)
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if id := identOfType(node.Type); id != "" && isForbiddenStub(id) {
				out = append(out, Finding{
					Rule:    "F50",
					File:    path,
					Line:    fset.Position(node.Pos()).Line,
					Message: fmt.Sprintf("forbidden no-op stub composite literal %q in production code; remove the stub or move it to a *_test.go file", id+"{}"),
				})
			}
		case *ast.GenDecl:
			if node.Tok == token.TYPE {
				for _, spec := range node.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					if isForbiddenStub(ts.Name.Name) {
						out = append(out, Finding{
							Rule:    "F50",
							File:    path,
							Line:    fset.Position(ts.Pos()).Line,
							Message: fmt.Sprintf("forbidden stub type declaration %q in production code; declarations of test-time stubs belong in *_test.go", ts.Name.Name),
						})
					}
				}
			}
			if node.Tok == token.VAR {
				for _, spec := range node.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					out = append(out, checkPlaceholderVar(fset, path, vs)...)
				}
			}
		}
		return true
	})
	return out
}

// checkE2EImports flags any import path containing the segment
// "/e2e/" — these are test-side helper packages that must not be
// linked into the production binary.
func checkE2EImports(fset *token.FileSet, path string, file *ast.File) []Finding {
	var out []Finding
	for _, imp := range file.Imports {
		raw, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if strings.Contains(raw, "/e2e/") {
			out = append(out, Finding{
				Rule:    "F119",
				File:    path,
				Line:    fset.Position(imp.Pos()).Line,
				Message: fmt.Sprintf("production code imports e2e/ helper package %q; the production binary must not link e2e fixtures (see /e2e/ path segment)", raw),
			})
		}
	}
	return out
}

// checkPlaceholderVar flags `var _ I = I(nil)` — a self-conversion of
// nil to its own interface type, which is a placeholder admitting no
// real implementation is yet wired (Finding #49).
func checkPlaceholderVar(fset *token.FileSet, path string, vs *ast.ValueSpec) []Finding {
	if vs.Type == nil || len(vs.Values) == 0 {
		return nil
	}
	hasBlank := false
	for _, name := range vs.Names {
		if name.Name == "_" {
			hasBlank = true
			break
		}
	}
	if !hasBlank {
		return nil
	}
	declTypeIdent := identOfType(vs.Type)
	if declTypeIdent == "" {
		return nil
	}
	for _, val := range vs.Values {
		call, ok := val.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			continue
		}
		nilArg, ok := call.Args[0].(*ast.Ident)
		if !ok || nilArg.Name != "nil" {
			continue
		}
		if identOfType(call.Fun) == declTypeIdent {
			return []Finding{{
				Rule:    "F49",
				File:    path,
				Line:    fset.Position(val.Pos()).Line,
				Message: fmt.Sprintf("interface-nil placeholder `var _ %[1]s = %[1]s(nil)` in production code; replace with a real implementation or `var _ %[1]s = (*ConcreteType)(nil)`", declTypeIdent),
			}}
		}
	}
	return nil
}

// identOfType returns the printable type name for `T`, `*T`, and
// `pkg.T`, or the empty string for anything more complex (generics,
// maps, channels, etc.). The selector form is represented as
// "pkg.Name" so two selector expressions referring to the same
// interface compare equal under string equality.
func identOfType(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return identOfType(t.X)
	case *ast.ParenExpr:
		return identOfType(t.X)
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		if !ok {
			return ""
		}
		return pkg.Name + "." + t.Sel.Name
	}
	return ""
}

func isForbiddenStub(name string) bool {
	for _, b := range forbiddenStubIdents {
		if name == b {
			return true
		}
	}
	return false
}
