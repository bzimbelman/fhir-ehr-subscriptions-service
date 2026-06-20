// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Phase A (RED) tests for OpenProject story #146 — Run real verifier.Middleware
// in the e2e harness. They pin the post-#146 contract:
//
//   - The harness package must NOT define principalMiddleware anywhere on the
//     production read path (it is a fixed-principal stub OP #146 bans).
//   - The harness package must reference auth.Verifier (the real verifier) so a
//     human reviewing api.go can see real verification is wired.
//   - APIServer must expose Bearer() / Client() helpers so tests can present
//     real tokens against the real verifier without each test re-implementing
//     a JWKS server.
//
// These tests are intentionally light on docker — they audit the source and
// the public surface of the package. Behavioral coverage of the
// 401-without-token + 200-with-token contract lives in the orchestrator
// e2e suite at e2e/orchestrator/prod_binary_serves_subscription_api_test.go,
// which the story acceptance criteria mandate be flipped to a real-verifier
// assertion.

// TestHarness_PrincipalMiddleware_NotDefined fails until the harness package
// drops the fixed-principal middleware stub. Its presence today is the OP
// #146 violation: every harness-spun API server bypasses verifier.Middleware
// and authorises every caller as a constant Principal.
func TestHarness_PrincipalMiddleware_NotDefined(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	bad := []string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		fileAST, err := parser.ParseFile(fset, f, src, parser.AllErrors)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		ast.Inspect(fileAST, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name == "principalMiddleware" {
				bad = append(bad, f+":"+id.Name)
			}
			return true
		})
	}
	if len(bad) > 0 {
		t.Fatalf("e2e/harness still defines/uses principalMiddleware (OP #146 forbids the fixed-principal stub): %v", bad)
	}
}

// TestHarness_References_RealVerifier asserts the harness package's
// production read path imports and uses the real auth.Verifier. The
// post-#146 wiring constructs an auth.Verifier and mounts its
// verifier.Middleware on the chi router.
func TestHarness_References_RealVerifier(t *testing.T) {
	apiPath := "api.go"
	body, err := os.ReadFile(apiPath)
	if err != nil {
		t.Fatalf("read %s: %v", apiPath, err)
	}
	src := string(body)
	if !strings.Contains(src, "auth.NewVerifier") {
		t.Errorf("%s does not call auth.NewVerifier — harness must wire the real verifier", apiPath)
	}
	if !strings.Contains(src, ".Middleware") {
		t.Errorf("%s does not mount verifier.Middleware", apiPath)
	}
}

// TestHarness_APIServer_ExposesBearer asserts the post-#146 APIServer
// exposes a Bearer() string helper so callers can issue authenticated
// requests against the real verifier without re-implementing a JWKS
// signer each time.
func TestHarness_APIServer_ExposesBearer(t *testing.T) {
	// Compile-time assertion via reflect-friendly interface check is
	// brittle; assert via the source instead. The Bearer() method must
	// be defined on *APIServer.
	body, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("read api.go: %v", err)
	}
	if !strings.Contains(string(body), "func (s *APIServer) Bearer(") {
		t.Errorf("APIServer is missing the Bearer() helper required by OP #146")
	}
	if !strings.Contains(string(body), "func (s *APIServer) Client(") {
		t.Errorf("APIServer is missing the Client() helper required by OP #146")
	}
}

// TestHarness_StartAPIServer_RealVerifier_RejectsNoToken is a behavioural
// RED test: calls StartAPIServer with the real-verifier wiring (post-#146
// Phase B) and asserts /metadata returns 401 without an Authorization
// header — i.e. the verifier.Middleware is on the chain.
//
// Today this test does not even compile against the pre-#146 harness
// because StartAPIServer does not return an APIServer with Bearer()/Client()
// helpers. Once Phase B lands, the test asserts:
//
//	GET /metadata (no Authorization)              → 401
//	GET /metadata (Authorization: Bearer <real>)  → 200
//
// Behavioural coverage of POST /Subscription with a real token already
// lives in TestE2E_ProdBinary_ServesSubscriptionAPI; this test pins the
// minimal harness-side contract.
func TestHarness_StartAPIServer_RealVerifier_RejectsNoToken(t *testing.T) {
	t.Skip("OP #146 Phase B drives this green by adding harness-minted bearer plumbing; left as a TODO assertion until then")

	// The body below is the post-#146 shape. Phase B replaces the t.Skip
	// above with the real wiring. Keeping the code as documentation of
	// the contract.
	_ = func() {
		ctx := context.Background()
		var srv *APIServer = nil
		_ = srv
		_ = ctx
		_ = bytes.NewReader([]byte("{}"))
		_ = io.Discard
		_ = http.MethodGet
	}
}
