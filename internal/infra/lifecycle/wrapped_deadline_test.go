// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// S-15 #8: isDeadlineExceeded must classify wrapped sentinels via
// errors.Is, not pointer equality.
func TestIsDeadlineExceeded_WrappedSentinels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain DeadlineExceeded", context.DeadlineExceeded, true},
		{"plain Canceled", context.Canceled, true},
		{"wrapped DeadlineExceeded", fmt.Errorf("wrap: %w", context.DeadlineExceeded), true},
		{"wrapped Canceled", fmt.Errorf("wrap: %w", context.Canceled), true},
		{"unrelated error", errors.New("other"), false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := isDeadlineExceeded(c.err); got != c.want {
				t.Errorf("isDeadlineExceeded(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
