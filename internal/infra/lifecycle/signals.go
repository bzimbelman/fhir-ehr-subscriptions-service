// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// signalDispatcher routes OS signals to the lifecycle module. It is the
// seam between the OS-level signal.Notify channel and RequestShutdown so
// tests can drive dispatchSignal directly instead of raising real OS
// signals into the test runner (LLD §9).
type signalDispatcher struct {
	mod *LifecycleModule
}

// newSignalDispatcher constructs a dispatcher bound to the given module.
func newSignalDispatcher(mod *LifecycleModule) *signalDispatcher {
	return &signalDispatcher{mod: mod}
}

// dispatchSignal maps a signal to a shutdown reason and forwards to
// RequestShutdown. Signals other than SIGTERM and SIGINT are ignored —
// SIGKILL is non-recoverable (LLD §9), and the deployment is free to
// reserve other signals (e.g. SIGHUP for config reload) for unrelated
// purposes.
func (d *signalDispatcher) dispatchSignal(sig os.Signal) {
	reason, ok := signalReason(sig)
	if !ok {
		return
	}
	d.mod.RequestShutdown(context.Background(), reason)
}

// signalReason maps the OS signal to the structured reason recorded in
// ShutdownReport.Reason and the fhir_subs_lifecycle_shutdown_initiated_total
// metric label. Returns ok=false for signals the dispatcher ignores.
func signalReason(sig os.Signal) (string, bool) {
	switch sig {
	case syscall.SIGTERM:
		return "sigterm", true
	case syscall.SIGINT:
		return "sigint", true
	default:
		return "", false
	}
}

// installSignalHandlers wires SIGTERM and SIGINT to a fresh
// signalDispatcher and starts a goroutine that forwards every signal it
// receives. The goroutine exits when ctx fires; SIGKILL is never received
// here (LLD §9).
//
// The function is safe to call once per LifecycleModule.
func installSignalHandlers(ctx context.Context, mod *LifecycleModule) error {
	d := newSignalDispatcher(mod)
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case sig := <-ch:
				d.dispatchSignal(sig)
			case <-ctx.Done():
				return
			case <-mod.exitDone:
				return
			}
		}
	}()
	return nil
}
