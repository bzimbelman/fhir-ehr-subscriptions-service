// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sync"
)

// Version is the build version. Overridden via -ldflags '-X main.Version=...'
// at link time. After main starts it is logically immutable; tests use
// SetBuildInfoForTest to mutate it under the package-level lock.
var Version = "dev"

// Commit is the build commit hash. Overridden via -ldflags '-X main.Commit=...'
// at link time. Same immutability contract as Version.
var Commit = "dev"

// buildInfoMu guards reads and (test-only) writes of Version/Commit so the
// race detector does not fire when other goroutines (the startup banner,
// the API CapabilityStatement publisher, the version-flag handler) read
// the package globals while a test mutates them. OP #211.
var buildInfoMu sync.RWMutex

// GetVersion returns the current build version under the read lock.
func GetVersion() string {
	buildInfoMu.RLock()
	defer buildInfoMu.RUnlock()
	return Version
}

// GetCommit returns the current build commit under the read lock.
func GetCommit() string {
	buildInfoMu.RLock()
	defer buildInfoMu.RUnlock()
	return Commit
}

// SetBuildInfoForTest swaps Version and Commit atomically and returns a
// restore function the caller MUST defer to put the previous values back.
// Only test code should call this. Production callers MUST use the
// link-time -ldflags injection instead.
func SetBuildInfoForTest(version, commit string) (restore func()) {
	buildInfoMu.Lock()
	prevV, prevC := Version, Commit
	Version = version
	Commit = commit
	buildInfoMu.Unlock()
	return func() {
		buildInfoMu.Lock()
		Version = prevV
		Commit = prevC
		buildInfoMu.Unlock()
	}
}

// versionString returns the formatted version banner used by --version and the
// startup banner.
func versionString() string {
	return fmt.Sprintf("fhir-subs %s (commit %s)", GetVersion(), GetCommit())
}
