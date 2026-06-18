// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package redaction_test

import (
	"reflect"
	"sync"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// TestRedactScalarAtSensitivePath: a value at a tagged path becomes [redacted].
func TestRedactScalarAtSensitivePath(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("storage.postgres.url")
	in := map[string]interface{}{
		"storage": map[string]interface{}{
			"postgres": map[string]interface{}{
				"url":       "postgres://operator-baked",
				"pool_size": 16,
			},
		},
	}
	out := m.Redact(in, "")
	got := out.(map[string]interface{})["storage"].(map[string]interface{})["postgres"].(map[string]interface{})
	if got["url"] != redaction.Redacted {
		t.Fatalf("url should be redacted; got %#v", got["url"])
	}
	if got["pool_size"] != 16 {
		t.Fatalf("pool_size should pass through; got %#v", got["pool_size"])
	}
}

// TestRedactObjectRecursesIntoMap: redaction walks nested objects when no
// ancestor is tagged.
func TestRedactObjectRecursesIntoMap(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("a.b.secret")
	in := map[string]interface{}{
		"a": map[string]interface{}{
			"b": map[string]interface{}{
				"secret": "shh",
				"public": "ok",
			},
		},
	}
	got := m.Redact(in, "")
	b := got.(map[string]interface{})["a"].(map[string]interface{})["b"].(map[string]interface{})
	if b["secret"] != redaction.Redacted {
		t.Fatalf("secret leaf not redacted: %#v", b["secret"])
	}
	if b["public"] != "ok" {
		t.Fatalf("public leaf must pass through: %#v", b["public"])
	}
}

// TestRedactObjectAtSensitivePathRecursesNot: when an *object* itself is
// tagged sensitive, the entire sub-tree is replaced — not walked. (This is
// the safety net for whole-object credentials.)
func TestRedactObjectAtSensitivePathReplaces(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("auth.client_registry.0")
	in := map[string]interface{}{
		"auth": map[string]interface{}{
			"client_registry": []interface{}{
				map[string]interface{}{"id": "x", "scopes": []interface{}{"a", "b"}},
			},
		},
	}
	got := m.Redact(in, "")
	cr := got.(map[string]interface{})["auth"].(map[string]interface{})["client_registry"].([]interface{})
	if cr[0] != redaction.Redacted {
		t.Fatalf("tagged object must be replaced wholesale; got %#v", cr[0])
	}
}

// TestRedactArrayRecurses: arrays walk by integer index.
func TestRedactArrayRecurses(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("auth.trusted_issuers.1.audience")
	in := map[string]interface{}{
		"auth": map[string]interface{}{
			"trusted_issuers": []interface{}{
				map[string]interface{}{"audience": "ok-0"},
				map[string]interface{}{"audience": "secret-1"},
				map[string]interface{}{"audience": "ok-2"},
			},
		},
	}
	got := m.Redact(in, "")
	ti := got.(map[string]interface{})["auth"].(map[string]interface{})["trusted_issuers"].([]interface{})
	if ti[0].(map[string]interface{})["audience"] != "ok-0" {
		t.Fatalf("ti[0] must pass through")
	}
	if ti[1].(map[string]interface{})["audience"] != redaction.Redacted {
		t.Fatalf("ti[1].audience must be redacted; got %#v", ti[1])
	}
	if ti[2].(map[string]interface{})["audience"] != "ok-2" {
		t.Fatalf("ti[2] must pass through")
	}
}

// TestRedactConcurrent: TagSensitive + IsSensitive are safe to call concurrently.
func TestRedactConcurrent(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	const N = 200
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			m.TagSensitive("p")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_ = m.IsSensitive("p")
		}
	}()
	wg.Wait()
	if !m.IsSensitive("p") {
		t.Fatalf("expected p to be tagged after concurrent run")
	}
}

// TestCloneIndependent: a cloned map is independent — tagging on one does not
// affect the other.
func TestCloneIndependent(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("a")
	c := m.Clone()
	c.TagSensitive("b")
	if m.IsSensitive("b") {
		t.Fatalf("clone leak")
	}
	if !c.IsSensitive("a") {
		t.Fatalf("clone must contain prior tags")
	}
}

// TestPathHelpers: JoinPath / JoinIndex shape stable paths used across the
// secrets resolver, validator and redaction walker.
func TestPathHelpers(t *testing.T) {
	t.Parallel()
	if redaction.JoinPath("", "deployment") != "deployment" {
		t.Fatalf("JoinPath empty base failure")
	}
	if redaction.JoinPath("a.b", "c") != "a.b.c" {
		t.Fatalf("JoinPath failure")
	}
	if redaction.JoinIndex("auth.trusted_issuers", 0) != "auth.trusted_issuers.0" {
		t.Fatalf("JoinIndex failure")
	}
}

// TestMergeFromSetUnion: MergeFrom set-unions another map's paths in-place.
func TestMergeFromSetUnion(t *testing.T) {
	t.Parallel()
	a := redaction.NewMap()
	a.TagSensitive("a")
	b := redaction.NewMap()
	b.TagSensitive("b")
	a.MergeFrom(b)
	if !a.IsSensitive("a") || !a.IsSensitive("b") {
		t.Fatalf("MergeFrom should union: %v", a.Paths())
	}
	if b.IsSensitive("a") {
		t.Fatalf("MergeFrom must not mutate the source")
	}
}

// TestMergeFromNil tolerates a nil source.
func TestMergeFromNil(t *testing.T) {
	t.Parallel()
	a := redaction.NewMap()
	a.TagSensitive("a")
	a.MergeFrom(nil)
	if !a.IsSensitive("a") {
		t.Fatalf("nil merge should be no-op")
	}
}

// TestRedactPreservesShape: redaction returns a fresh structure, never mutates
// the input.
func TestRedactPreservesShape(t *testing.T) {
	t.Parallel()
	m := redaction.NewMap()
	m.TagSensitive("x")
	in := map[string]interface{}{"x": "secret", "y": "public"}
	original := map[string]interface{}{"x": "secret", "y": "public"}
	_ = m.Redact(in, "")
	if !reflect.DeepEqual(in, original) {
		t.Fatalf("Redact mutated input: %#v", in)
	}
}
