// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package main

import "fmt"

// Version is the build version. Overridden via -ldflags '-X main.Version=...'.
var Version = "dev"

// Commit is the build commit hash. Overridden via -ldflags '-X main.Commit=...'.
var Commit = "dev"

// versionString returns the formatted version banner used by --version and the
// startup banner.
func versionString() string {
	return fmt.Sprintf("fhir-subs %s (commit %s)", Version, Commit)
}
