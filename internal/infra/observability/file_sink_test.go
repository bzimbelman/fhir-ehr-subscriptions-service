// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/observability/audit"
)

// B-34: the audit file sink must call fsync on each write in the
// default "every_write" mode so a power loss between Emit return and
// the kernel page-cache flush does not lose recent rows.
func TestFileSink_EveryWrite_FsyncsAfterEachWrite(t *testing.T) {
	t.Parallel()
	w := &countingWriteSyncer{}
	sink := newFileSinkWithSyncer(w, fileSinkOptions{Mode: fileSyncEveryWrite})

	for i := 0; i < 5; i++ {
		err := sink.Emit(context.Background(), audit.Event{
			ActorKind: "system", Action: "x", Outcome: "success",
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if got := w.writeCount.Load(); got != 5 {
		t.Fatalf("writeCount = %d; want 5", got)
	}
	if got := w.syncCount.Load(); got != 5 {
		t.Fatalf("syncCount = %d; want 5 (every-write mode must fsync per Emit)", got)
	}
}

// B-34: in "batched" mode the sink fsyncs periodically (or at Close),
// not on every write. The opt-in mode trades durability for throughput.
func TestFileSink_Batched_DoesNotFsyncEveryWrite(t *testing.T) {
	t.Parallel()
	w := &countingWriteSyncer{}
	sink := newFileSinkWithSyncer(w, fileSinkOptions{
		Mode:          fileSyncBatched,
		BatchInterval: 10 * time.Second, // long enough not to tick during test
	})

	for i := 0; i < 50; i++ {
		err := sink.Emit(context.Background(), audit.Event{
			ActorKind: "system", Action: "x", Outcome: "success",
		})
		if err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if got := w.writeCount.Load(); got != 50 {
		t.Fatalf("writeCount = %d; want 50", got)
	}
	if got := w.syncCount.Load(); got != 0 {
		t.Fatalf("syncCount = %d; want 0 in batched mode without timer tick", got)
	}

	// Closing the sink flushes pending writes — durability guarantee
	// even in batched mode for clean shutdown.
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := w.syncCount.Load(); got < 1 {
		t.Fatalf("after Close, syncCount = %d; want >= 1", got)
	}
}

// countingWriteSyncer is a fake that counts Write and Sync calls. It
// substitutes for *os.File in unit tests so we can assert the sink's
// fsync behavior without writing to disk.
type countingWriteSyncer struct {
	mu         sync.Mutex
	buf        strings.Builder
	writeCount atomic.Int64
	syncCount  atomic.Int64
}

func (w *countingWriteSyncer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeCount.Add(1)
	return w.buf.Write(p)
}

func (w *countingWriteSyncer) Sync() error {
	w.syncCount.Add(1)
	return nil
}

func (w *countingWriteSyncer) Close() error { return nil }
