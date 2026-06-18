// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
)

// signalDispatcher — stub. Real implementation lands in the
// signal-handler GREEN commit.
type signalDispatcher struct {
	mod *LifecycleModule
}

// newSignalDispatcher constructs a dispatcher bound to the given module.
// Tests use this directly; production callers go through
// installSignalHandlers.
func newSignalDispatcher(mod *LifecycleModule) *signalDispatcher {
	return &signalDispatcher{mod: mod}
}

// dispatchSignal is the entry point invoked by both the OS signal
// goroutine and the test harness.
func (d *signalDispatcher) dispatchSignal(sig os.Signal) {
	_ = sig
}

// installSignalHandlers wires SIGTERM and SIGINT to the dispatcher.
func installSignalHandlers(ctx context.Context, mod *LifecycleModule) error {
	_ = ctx
	_ = mod
	return nil
}
