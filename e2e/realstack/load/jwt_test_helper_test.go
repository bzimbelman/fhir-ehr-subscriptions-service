// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e_realstack

package load_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// jwtIssuerClaim parses the issuer (`iss`) claim from a compact-form
// JWT without verifying the signature. The H3 acceptance criteria
// only need to confirm the token came from the real Keycloak realm —
// the binary's verifier middleware is what cryptographically validates
// it on the wire path.
func jwtIssuerClaim(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("token does not have three dot-separated segments")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some Keycloak builds emit padded base64; tolerate.
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return "", fmt.Errorf("decode payload: %w", err)
		}
	}
	var claims struct {
		Iss string `json:"iss"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("unmarshal payload: %w", err)
	}
	return claims.Iss, nil
}
