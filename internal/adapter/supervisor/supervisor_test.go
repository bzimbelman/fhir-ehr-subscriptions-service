// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package supervisor_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
)

// fakeWorker is a Worker test double the supervisor drives. Each call to Run
// pulls the next behavior off `behaviors`; once exhausted it blocks on ctx.
type fakeWorker struct {
	mu        sync.Mutex
	calls     int32
	behaviors []func(ctx context.Context) error
}

func (f *fakeWorker) Run(ctx context.Context) error {
	n := atomic.AddInt32(&f.calls, 1)
	f.mu.Lock()
	var fn func(ctx context.Context) error
	if int(n) <= len(f.behaviors) {
		fn = f.behaviors[n-1]
	}
	f.mu.Unlock()
	if fn == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	return fn(ctx)
}

// Story #62: a worker that panics is recovered and restarted by the supervisor;
// the supervisor itself does not crash and a subsequent run is observed.
func TestSupervisor_RestartsOnPanic(t *testing.T) {
	t.Parallel()
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(_ context.Context) error { panic("boom") },
			// Second run: signal we got here, then block until cancel.
			nil,
		},
	}
	sv, err := supervisor.New(supervisor.Options{
		AdapterID:      "test-adapter",
		Worker:         w,
		BackoffInitial: 1 * time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = sv.Start(ctx)
		close(done)
	}()

	// Wait for the second Run call (the post-panic restart).
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&w.calls) < 2 {
		select {
		case <-deadline:
			t.Fatalf("supervisor did not restart after panic: calls=%d", atomic.LoadInt32(&w.calls))
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done

	if got := sv.Status().RestartCount; got < 1 {
		t.Fatalf("expected RestartCount >= 1, got %d", got)
	}
}

// Story #62: when a worker returns an error (non-panic), supervisor restarts it
// just like a panic. Both paths increment RestartCount.
func TestSupervisor_RestartsOnError(t *testing.T) {
	t.Parallel()
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(_ context.Context) error { return errors.New("boom") },
			nil,
		},
	}
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: 1 * time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = sv.Start(ctx)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&w.calls) < 2 {
		select {
		case <-deadline:
			t.Fatalf("no restart after error: calls=%d", atomic.LoadInt32(&w.calls))
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if sv.Status().RestartCount < 1 {
		t.Fatalf("expected RestartCount>=1, got %d", sv.Status().RestartCount)
	}
}

// Story #62: backoff between restarts grows exponentially up to BackoffMax.
// Use a fake clock-free check: record sleep durations via Sleep injection.
func TestSupervisor_ExponentialBackoffCappedAtMax(t *testing.T) {
	t.Parallel()
	var sleeps []time.Duration
	var mu sync.Mutex
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(_ context.Context) error { return errors.New("e1") },
			func(_ context.Context) error { return errors.New("e2") },
			func(_ context.Context) error { return errors.New("e3") },
			func(_ context.Context) error { return errors.New("e4") },
			nil, // 5th run blocks
		},
	}
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     40 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
		Sleep: func(_ context.Context, d time.Duration) {
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = sv.Start(ctx)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&w.calls) < 5 {
		select {
		case <-deadline:
			t.Fatalf("did not reach 5 calls: %d", atomic.LoadInt32(&w.calls))
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) < 4 {
		t.Fatalf("expected >= 4 sleeps, got %d (%v)", len(sleeps), sleeps)
	}
	want := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		40 * time.Millisecond,
	}
	for i, w := range want {
		if sleeps[i] != w {
			t.Errorf("sleeps[%d]=%v, want %v (full=%v)", i, sleeps[i], w, sleeps)
		}
	}
}

// Story #62: jitter is applied as a multiplicative factor in [1, 1+max], so
// the actual sleep stays >= base and <= base*(1+jitterMax).
func TestSupervisor_JitterStaysBounded(t *testing.T) {
	t.Parallel()
	var sleeps []time.Duration
	var mu sync.Mutex
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(_ context.Context) error { return errors.New("e1") },
			func(_ context.Context) error { return errors.New("e2") },
			nil,
		},
	}
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: 10 * time.Millisecond,
		BackoffMax:     40 * time.Millisecond,
		JitterMax:      0.5,                         // up to +50%
		Jitter:         func() float64 { return 1 }, // pin to max
		Sleep: func(_ context.Context, d time.Duration) {
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = sv.Start(ctx)
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&w.calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("did not reach 3 calls: %d", atomic.LoadInt32(&w.calls))
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(sleeps) < 2 {
		t.Fatalf("expected >=2 sleeps, got %d", len(sleeps))
	}
	// First base = 10ms, jitter 1.0 * 0.5 = +5ms => 15ms.
	if sleeps[0] != 15*time.Millisecond {
		t.Errorf("sleeps[0]=%v, want 15ms", sleeps[0])
	}
	// Second base = 20ms, +50% => 30ms.
	if sleeps[1] != 30*time.Millisecond {
		t.Errorf("sleeps[1]=%v, want 30ms", sleeps[1])
	}
}

// Story #62: Stop(ctx) cancels the worker and returns once Run has exited,
// bounded by the supplied deadline. A clean (cancel-aware) worker exits well
// inside the deadline.
func TestSupervisor_StopGracefulWithinDeadline(t *testing.T) {
	t.Parallel()
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: 1 * time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_ = sv.Start(ctx)
		close(done)
	}()
	// Wait until the worker is in Run.
	deadline := time.After(time.Second)
	for atomic.LoadInt32(&w.calls) < 1 {
		select {
		case <-deadline:
			t.Fatalf("worker never entered Run")
		case <-time.After(time.Millisecond):
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer stopCancel()
	t0 := time.Now()
	if err := sv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(t0); elapsed > 200*time.Millisecond {
		t.Fatalf("Stop took %v (deadline 200ms)", elapsed)
	}
	cancel()
	<-done
	if !sv.Status().Stopped {
		t.Fatal("expected Stopped=true after Stop")
	}
}

// Story #62: a hung worker that does not honor cancellation triggers Stop's
// deadline path. Stop returns ErrStopTimeout (or wraps ctx error), and the
// status reflects that the supervisor is no longer running.
func TestSupervisor_StopHungWorkerHitsDeadline(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(_ context.Context) error {
				<-hold // ignores ctx; the test releases this at the end.
				return nil
			},
		},
	}
	defer close(hold)
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: 1 * time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sv.Start(ctx) }()
	for atomic.LoadInt32(&w.calls) < 1 {
		time.Sleep(time.Millisecond)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stopCancel()
	err := sv.Stop(stopCtx)
	if !errors.Is(err, supervisor.ErrStopTimeout) {
		t.Fatalf("Stop: got %v, want ErrStopTimeout", err)
	}
}

// Story #62: the supervisor exposes a State enum (Idle/Running/Restarting/Stopped)
// and a per-adapter health observation through Status(). HealthTick fires the
// tick callback periodically with the current state.
func TestSupervisor_HealthTick(t *testing.T) {
	t.Parallel()
	w := &fakeWorker{
		behaviors: []func(ctx context.Context) error{
			func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}
	var ticks int32
	var sawRunning atomic.Bool
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
		HealthInterval: 5 * time.Millisecond,
		OnHealthTick: func(s supervisor.Status) {
			atomic.AddInt32(&ticks, 1)
			if s.State == supervisor.StateRunning {
				sawRunning.Store(true)
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sv.Start(ctx); close(done) }()
	deadline := time.After(time.Second)
	for atomic.LoadInt32(&ticks) < 3 {
		select {
		case <-deadline:
			t.Fatalf("expected >=3 ticks, got %d", atomic.LoadInt32(&ticks))
		case <-time.After(2 * time.Millisecond):
		}
	}
	if !sawRunning.Load() {
		t.Fatal("expected at least one tick to observe StateRunning")
	}
	cancel()
	<-done
}

// Story #62: a New that reaches BackoffMax keeps applying BackoffMax — does
// not overflow into negative durations on long-running outages.
func TestSupervisor_BackoffNoOverflow(t *testing.T) {
	t.Parallel()
	w := &fakeWorker{}
	for i := 0; i < 100; i++ {
		w.behaviors = append(w.behaviors, func(_ context.Context) error { return errors.New("e") })
	}
	w.behaviors = append(w.behaviors, nil)
	var maxObserved time.Duration
	var mu sync.Mutex
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "x",
		Worker:         w,
		BackoffInitial: time.Millisecond,
		BackoffMax:     8 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
		Sleep: func(_ context.Context, d time.Duration) {
			mu.Lock()
			if d > maxObserved {
				maxObserved = d
			}
			mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = sv.Start(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt32(&w.calls) < 50 {
		select {
		case <-deadline:
			t.Fatalf("did not reach 50 calls: %d", atomic.LoadInt32(&w.calls))
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if maxObserved > 8*time.Millisecond {
		t.Fatalf("backoff exceeded cap: maxObserved=%v cap=8ms", maxObserved)
	}
	if maxObserved <= 0 {
		t.Fatalf("backoff went non-positive: %v", maxObserved)
	}
}

// Story #62: New rejects nil Worker and zero/negative timing knobs.
func TestSupervisor_NewValidatesOptions(t *testing.T) {
	t.Parallel()
	if _, err := supervisor.New(supervisor.Options{}); err == nil {
		t.Fatal("expected error for nil Worker")
	}
	if _, err := supervisor.New(supervisor.Options{
		Worker:         &fakeWorker{},
		BackoffInitial: 0,
		BackoffMax:     time.Millisecond,
	}); err == nil {
		t.Fatal("expected error for zero BackoffInitial")
	}
	if _, err := supervisor.New(supervisor.Options{
		Worker:         &fakeWorker{},
		BackoffInitial: 2 * time.Millisecond,
		BackoffMax:     time.Millisecond,
	}); err == nil {
		t.Fatal("expected error for BackoffMax < BackoffInitial")
	}
}

// Story #62: Status returns a snapshot tagged with the adapter id so callers
// can label metrics correctly.
func TestSupervisor_StatusCarriesAdapterID(t *testing.T) {
	t.Parallel()
	sv, _ := supervisor.New(supervisor.Options{
		AdapterID:      "epic",
		Worker:         &fakeWorker{},
		BackoffInitial: time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
		Jitter:         func() float64 { return 0 },
	})
	if got := sv.Status().AdapterID; got != "epic" {
		t.Fatalf("AdapterID=%q, want epic", got)
	}
}
