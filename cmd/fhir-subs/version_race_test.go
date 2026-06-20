// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"sync"
	"testing"
)

// TestBuildInfo_ConcurrentReadsRaceFree exercises GetVersion()/GetCommit() under -race
// to assert the build-info accessors are safe to call from many goroutines
// concurrently. OP #211: Version/Commit must not be plain mutable globals;
// readers race against test-time mutation.
func TestBuildInfo_ConcurrentReadsRaceFree(t *testing.T) {
	t.Parallel()

	const goroutines = 32
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = GetVersion()
				_ = GetCommit()
				_ = versionString()
			}
		}()
	}
	wg.Wait()
}
