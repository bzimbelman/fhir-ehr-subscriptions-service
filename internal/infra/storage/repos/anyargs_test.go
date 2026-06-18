// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package repos_test

import "github.com/pashagolub/pgxmock/v3"

// anyArgs returns n pgxmock.AnyArg matchers.
func anyArgs(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}
