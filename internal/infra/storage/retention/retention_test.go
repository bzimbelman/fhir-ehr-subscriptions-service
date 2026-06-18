// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package retention

import (
	"context"
	"testing"
)

func TestTickNilPoolErrors(t *testing.T) {
	t.Parallel()
	if err := Tick(context.Background(), nil, Config{}); err == nil {
		t.Error("expected error for nil pool")
	}
}
