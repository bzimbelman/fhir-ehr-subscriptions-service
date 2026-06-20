// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

// TestPipelineSupervisor_DrainHookRegisteredBeforeGoroutines covers
// OP #201. The supervisor drain hook MUST be registered with the
// lifecycle module BEFORE any `go sv.Start(...)` goroutine is
// launched. Otherwise a SIGTERM that fires between the first
// goroutine launch and the hook registration leaks supervisor
// goroutines past the shutdown sequencer's drain phase.
//
// We assert the source-level invariant via a tiny AST walk over
// supervised_pipeline.go: the position of the
// `pipeline.supervisors.drain` RegisterShutdown call MUST come
// before the `for _, sp := range specs` loop that fans out the
// supervisors. A pure-runtime test of this race would require
// stopping the goroutines mid-launch, which Go does not expose.
func TestPipelineSupervisor_DrainHookRegisteredBeforeGoroutines(t *testing.T) {
	t.Parallel()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed; cannot locate source")
	}
	src := filepath.Join(filepath.Dir(thisFile), "supervised_pipeline.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.AllErrors)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	var fn *ast.FuncDecl
	for _, d := range file.Decls {
		if f, ok := d.(*ast.FuncDecl); ok && f.Name.Name == "buildSupervisedPipeline" {
			fn = f
			break
		}
	}
	if fn == nil {
		t.Fatalf("buildSupervisedPipeline not found in %s", src)
	}

	var (
		drainHookPos token.Pos
		specsLoopPos token.Pos
	)
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// Look for RegisterShutdown calls whose Name field is the
			// literal "pipeline.supervisors.drain".
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "RegisterShutdown" {
				return true
			}
			if !drainHookPos.IsValid() && callRegistersHookNamed(node, "pipeline.supervisors.drain") {
				drainHookPos = node.Pos()
			}
		case *ast.RangeStmt:
			// `for _, sp := range specs` is the goroutine fan-out loop.
			if specsLoopPos.IsValid() {
				return true
			}
			ident, ok := node.X.(*ast.Ident)
			if ok && ident.Name == "specs" {
				specsLoopPos = node.Pos()
			}
		}
		return true
	})

	if !drainHookPos.IsValid() {
		t.Fatalf("did not find RegisterShutdown(...pipeline.supervisors.drain...) in buildSupervisedPipeline")
	}
	if !specsLoopPos.IsValid() {
		t.Fatalf("did not find `for _, sp := range specs` in buildSupervisedPipeline")
	}
	if drainHookPos > specsLoopPos {
		drain := fset.Position(drainHookPos)
		loop := fset.Position(specsLoopPos)
		t.Errorf("OP #201 invariant violated: drain hook registered at %s:%d AFTER goroutine fan-out loop at %s:%d. SIGTERM during construction may leak supervisor goroutines.",
			drain.Filename, drain.Line, loop.Filename, loop.Line)
	}
}

func callRegistersHookNamed(call *ast.CallExpr, want string) bool {
	if len(call.Args) != 1 {
		return false
	}
	composite, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, elt := range composite.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok {
			continue
		}
		// strip the surrounding quote characters
		if len(lit.Value) < 2 {
			return false
		}
		return lit.Value[1:len(lit.Value)-1] == want
	}
	return false
}
