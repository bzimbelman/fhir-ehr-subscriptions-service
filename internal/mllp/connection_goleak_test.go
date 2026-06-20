// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

// OP #228: startRead spawns a goroutine that does the blocking
// conn.Read; on ctx cancel the main loop returns and that goroutine
// must drain into the size-1 buffered readCh without leaking. We
// verify no goroutines leak after a sequence of cancel-mid-read cycles.
//
// The invariant under test: readCh is buffered (size 1) so the read
// goroutine ALWAYS finds a slot to deposit its result, even after the
// main loop has stopped selecting on it. Without the buffer, the read
// goroutine would block forever on `readCh <- out` after ctx fires.

func TestHandleConnection_NoGoroutineLeak_OnCtxCancel(t *testing.T) {
	defer goleak.VerifyNone(t,
		// Tolerate the standard runtime goroutines goleak ignores by
		// default; nothing beyond those should remain.
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)

	const cycles = 25
	for i := 0; i < cycles; i++ {
		runCancelMidReadCycle(t)
	}
}

func runCancelMidReadCycle(t *testing.T) {
	t.Helper()

	ep := EndpointConfig{Name: "leak-test"}
	cfg := defaultConfig(ep)
	// Long idle timeout so the conn.Read genuinely blocks until ctx
	// fires (otherwise the read returns on the deadline and the cycle
	// is not exercising the cancel-mid-read path).
	cfg.ReadIdleTimeout = 10 * time.Second
	cfg.FrameAssemblyTimeout = 10 * time.Second

	server, client := net.Pipe()
	defer client.Close()

	p := &fakePersister{}
	m := newFakeMetrics()

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		HandleConnection(ctx, server, ep, cfg, p, m, "127.0.0.1:5050")
	}()

	// Give the read goroutine a moment to park inside conn.Read so
	// the ctx-cancel path is the one being exercised.
	time.Sleep(2 * time.Millisecond)
	cancel()

	wg.Wait()
}
