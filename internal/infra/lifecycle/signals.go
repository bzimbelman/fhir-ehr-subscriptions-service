// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import "context"

// installSignalHandlers — stub. Real implementation lands in the
// signal-handler GREEN commit.
func installSignalHandlers(ctx context.Context, mod *LifecycleModule) error {
	_ = ctx
	_ = mod
	return nil
}
