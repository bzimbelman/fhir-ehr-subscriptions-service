// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package reload

import (
	"reflect"
	"testing"
)

// S-15 #6: a literal "." in a config key (e.g. a hostname-shaped
// subscriber id) survives splitPath when escaped with a backslash.
func TestSplitPath_EscapedDot(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a.b.c", []string{"a", "b", "c"}},
		{`a\.b`, []string{"a.b"}},
		{`subscribers.acme\.com.url`, []string{"subscribers", "acme.com", "url"}},
		{`a\\b`, []string{`a\b`}},
		{`a\\.b`, []string{`a\`, "b"}},
	}
	for _, c := range cases {
		got := splitPath(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
