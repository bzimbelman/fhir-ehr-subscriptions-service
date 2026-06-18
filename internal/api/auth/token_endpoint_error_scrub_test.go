// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// TestTokenEndpoint_DiagnosticsDoNotLeakLibraryError pins B-8: the OAuth
// error path previously formatted err.Error() into the
// OperationOutcome.diagnostics body. golang-jwt/v5's error strings can
// reveal key ids, algorithm names, and other internals helpful to an
// offline attacker. The diagnostics surface for assertion failures must
// be a fixed, generic message.
func TestTokenEndpoint_DiagnosticsDoNotLeakLibraryError(t *testing.T) {
	t.Parallel()
	k := newKey(t)
	srv := jwksServer(t, k)
	clientID := "c1"
	te := newTokenEndpoint(t, "aud", "https://x/token", clientID, srv.URL+"/jwks", []string{"system/Subscription.r"})

	// Sign with a different key so jwt verification fails. The library
	// error string mentions "crypto/rsa: verification error" — assert
	// none of those library-internal phrases reach the wire.
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": clientID,
		"sub": clientID,
		"aud": "https://x/token",
		"jti": uuid.NewString(),
		"iat": now.Add(-1 * time.Minute).Unix(),
		"exp": now.Add(2 * time.Minute).Unix(),
	})
	tok.Header["kid"] = k.kid
	signed, err := tok.SignedString(other)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := postToken(t, te, tokenEndpointForm(signed))
	if rec.Code == 200 {
		t.Fatalf("expected error response; got 200 body=%s", rec.Body.String())
	}
	body := rec.Body.String()

	// golang-jwt error tokens, key-internal phrases, and stdlib crypto
	// errors must NOT appear on the wire.
	bannedPhrases := []string{
		"crypto/rsa",
		"rsa.PublicKey",
		"verification error",
		"token signature is invalid",
		"could not parse token",
		"jwt:",
		k.kid, // we passed a known kid; never echo it
		"keyfunc",
	}
	for _, ph := range bannedPhrases {
		if strings.Contains(body, ph) {
			t.Errorf("response leaks %q; body=%s", ph, body)
		}
	}
	// And the response should be FHIR-shaped.
	if !strings.Contains(body, "OperationOutcome") {
		t.Errorf("expected OperationOutcome; got %s", body)
	}
}

// TestTokenEndpoint_MalformedAssertion_DoesNotLeakInternals exercises a
// completely malformed assertion (not even three segments) and asserts
// the diagnostics field is generic.
func TestTokenEndpoint_MalformedAssertion_DoesNotLeakInternals(t *testing.T) {
	t.Parallel()
	te := newTokenEndpoint(t, "aud", "https://x/token", "c", "", nil)
	rec := postToken(t, te, tokenEndpointForm("not.a.jwt"))
	if rec.Code == 200 {
		t.Fatalf("expected error; got 200")
	}
	body := rec.Body.String()
	for _, ph := range []string{"jwt:", "token contains an invalid number of segments", "Parse"} {
		if strings.Contains(body, ph) {
			t.Errorf("response leaks %q; body=%s", ph, body)
		}
	}
}
