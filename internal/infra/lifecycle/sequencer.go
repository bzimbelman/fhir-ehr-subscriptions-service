// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
)

// sequencer — stub. Real implementation lands in the GREEN commit.

// newModuleForTest builds a LifecycleModule wired for in-process tests:
// the registry and sequencer goroutine are live, but the probe HTTP
// listener is not bound. Test files use this helper.
func newModuleForTest(cfg LifecycleConfig, lctx LifecycleContext) (*LifecycleModule, error) {
	return nil, errors.New("lifecycle.newModuleForTest: not implemented")
}

// stopForTest tears down whatever newModuleForTest started. Idempotent.
func (m *LifecycleModule) stopForTest() {}

// runShutdown is the sequencer body. It is a method so it can use the
// module's registry, metrics, logger, clock, and report storage.
func (m *LifecycleModule) runShutdown(ctx context.Context, reason string) {
	_ = ctx
	_ = reason
}
