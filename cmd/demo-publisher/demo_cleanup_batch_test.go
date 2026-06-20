// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDemoPublisher_DefaultAddrMatchesDemoMLLPBind pins the OP #156
// fix: the publisher's default --addr MUST resolve to the bridge's
// MLLP listener (`:2575`) so a contributor running
// `demo-publisher --catalog ...` against demo/docker-compose.yml
// connects without a flag override.
func TestDemoPublisher_DefaultAddrMatchesDemoMLLPBind(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `fs.String("addr", "127.0.0.1:2575"`) {
		t.Fatalf(`OP #156: demo-publisher --addr default must be 127.0.0.1:2575 (matches demo MLLP listener bind); got:
%s`, body)
	}
	if strings.Contains(body, `"127.0.0.1:6000"`) {
		t.Fatalf(`OP #156: demo-publisher must not still default to the stale 127.0.0.1:6000 port`)
	}
}

// TestDemoPublisher_NoE2EImports asserts OP #158: the operator-facing
// publisher MUST NOT import e2e/* test scaffolding. If those packages
// gain a build tag in the future, demo CLIs would otherwise stop
// compiling.
func TestDemoPublisher_NoE2EImports(t *testing.T) {
	t.Parallel()
	assertNoE2EImports(t, ".")
}

// assertNoE2EImports walks the package's non-test .go files and fails
// if any import path begins with `.../e2e/`.
func assertNoE2EImports(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "/e2e/") {
				t.Errorf("%s: forbidden e2e import %q (OP #158)", path, p)
			}
		}
	}
}
