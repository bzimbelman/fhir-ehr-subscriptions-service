// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
)

// readinessEntry pairs a check's name with its callable.
type readinessEntry struct {
	name  string
	check ReadinessCheck
}

// registry holds the in-memory state of the lifecycle module:
//   - the set of readiness checks registered by components,
//   - the set of shutdown hooks bucketed by phase,
//   - the shutdown_in_progress, panic_signaled, and startup_complete flags.
//
// All mutating operations are guarded by a mutex; concurrent registration
// from multiple goroutines is the documented call pattern (LLD §7).
type registry struct {
	// stub — implementation lives in the GREEN commit.
}

func newRegistry() *registry {
	return &registry{}
}

func (r *registry) registerReadiness(name string, check ReadinessCheck) error {
	return errors.New("lifecycle.registerReadiness: not implemented")
}

func (r *registry) registerShutdown(hook ShutdownHook) error {
	return errors.New("lifecycle.registerShutdown: not implemented")
}

func (r *registry) snapshotReadiness() []readinessEntry { return nil }

func (r *registry) hooksInPhase(phase Phase) []ShutdownHook { return nil }

func (r *registry) shutdownInProgress() bool { return false }

func (r *registry) startupComplete() bool { return false }

func (r *registry) panicSignaled() bool { return false }

func (r *registry) markShutdownInProgress() {}

func (r *registry) markStartupComplete() {}

func (r *registry) markPanicSignaled() {}

// runChecksConcurrently is exported only to the package; the readiness
// handler uses it. The aggregator is implemented in readiness.go.
//
// runChecksConcurrently runs every entry concurrently, with each entry
// raced against perCheckTimeout. A nil error means the check passed; a
// non-nil error means the check failed (failed[] carries the check name,
// not the error string — that is for log/metric labels).
func runChecksConcurrently(ctx context.Context, entries []readinessEntry, perCheckTimeout int64) []checkResult {
	return nil
}

type checkResult struct {
	name   string
	failed bool
	reason string
}
