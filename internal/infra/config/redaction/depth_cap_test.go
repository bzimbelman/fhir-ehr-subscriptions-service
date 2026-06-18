// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package redaction_test

import (
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/config/redaction"
)

// TestRedactDepthCap proves the redaction walker collapses pathological
// nesting to a sentinel rather than recursing past MaxRedactDepth.
// S-15 #3.
func TestRedactDepthCap(t *testing.T) {
	t.Parallel()

	// Build a map nested MaxRedactDepth+50 deep.
	tree := map[string]interface{}{}
	cur := tree
	for i := 0; i < redaction.MaxRedactDepth+50; i++ {
		next := map[string]interface{}{}
		cur["k"] = next
		cur = next
	}
	cur["leaf"] = "secret"

	m := redaction.NewMap()
	out := m.Redact(tree, "")
	// The walker must return without panicking and the deep tail must
	// be sentinelized.
	if out == nil {
		t.Fatal("expected non-nil result")
	}

	// Walk back down the (still well-formed) shallow prefix to make
	// sure it stayed structured up to the cap.
	depth := 0
	walk := out
	for {
		mp, ok := walk.(map[string]interface{})
		if !ok {
			break
		}
		next, has := mp["k"]
		if !has {
			break
		}
		walk = next
		depth++
		if depth > redaction.MaxRedactDepth+10 {
			t.Fatalf("walker did not honour depth cap (still nested at %d)", depth)
		}
	}

	if s, ok := walk.(string); !ok || s != redaction.RedactedTooDeep {
		t.Fatalf("expected RedactedTooDeep at the cap; got %T %v", walk, walk)
	}
}
