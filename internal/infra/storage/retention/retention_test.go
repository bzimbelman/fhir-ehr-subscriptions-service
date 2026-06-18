// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package retention

import (
	"testing"
)

func TestTickNilPoolErrors(t *testing.T) {
	t.Parallel()
	if err := Tick(t.Context(), nil, Config{}); err == nil {
		t.Error("expected error for nil pool")
	}
}
