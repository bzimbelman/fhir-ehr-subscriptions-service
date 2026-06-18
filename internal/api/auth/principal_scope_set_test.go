// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package auth_test

import (
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/auth"
)

// S-3 (HasScope O(n)): Principal.HasScope and HasAllScopes use a hashed
// set under the hood once Scopes is set, so lookups are O(1) regardless
// of the number of granted scopes. The test asserts behavior under load:
// a principal with many scopes still answers correctly and quickly.
func TestPrincipal_HasScope_LargeSetCorrect(t *testing.T) {
	t.Parallel()
	scopes := make([]string, 0, 1024)
	for i := 0; i < 1024; i++ {
		scopes = append(scopes, "system/Resource_"+itoa(i)+".r")
	}
	scopes = append(scopes, "system/Subscription.cruds")

	p := &auth.Principal{ClientID: "c", Scopes: scopes}

	if !p.HasScope("system/Subscription.cruds") {
		t.Fatalf("expected HasScope(system/Subscription.cruds) to be true")
	}
	if p.HasScope("system/Subscription.never") {
		t.Fatalf("HasScope returned true for unknown scope")
	}
	if !p.HasAllScopes("system/Resource_5.r", "system/Resource_900.r", "system/Subscription.cruds") {
		t.Fatalf("HasAllScopes returned false for present scopes")
	}
	if p.HasAllScopes("system/Subscription.cruds", "system/Resource_does_not_exist.r") {
		t.Fatalf("HasAllScopes returned true with missing scope")
	}
}

// itoa avoids importing strconv in a tiny test helper.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
