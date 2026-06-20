// Package main runs prod-stub-lint, a static AST check that fails the
// build if production code (cmd/, internal/) ships test-time stubs that
// the binary should not carry. Real software: it walks Go source with
// go/parser + go/ast (no mocks), and resolves t.Skip ages with real
// `git blame` invoked via os/exec.
//
// Rules enforced (each surfaces as a non-zero exit and a printed
// finding):
//
//   - F50: a forbidden no-op stub identifier appears in non-test code
//     under cmd/ or internal/. Identifiers blocked: defaultActivator,
//     noopReplayer, stubChannelActivator. Detected via composite
//     literals and top-level declarations.
//   - F49: an interface-typed placeholder of the form
//     `var _ I = I(nil)` (a self-conversion of nil to its own interface
//     type) appears in non-test code under cmd/ or internal/.
//   - F119: a production package under cmd/ or internal/ imports a
//     module path containing the path segment "/e2e/".
//   - F84: a t.Skip / t.Skipf / t.SkipNow call in a *_test.go file under
//     cmd/ or internal/ has a `git blame` author date older than 30
//     days.
//
// The lint walks source rooted at the repository root (auto-detected by
// finding go.mod), and exits 0 only when zero findings are reported.
//
// See OpenProject #264 (epic #91 — H9 ProductionStubLint).
package main
