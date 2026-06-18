// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package supervisor provides host-side worker lifecycle for adapter
// sub-components (Hl7MessageProcessor, FhirScanRunner, VendorAPIClient,
// HydrationService). The supervisor wraps a Worker, recovers panics,
// restarts on exit, applies exponential backoff with bounded jitter, and
// emits periodic health observations a metrics layer can consume.
//
// The supervisor is deliberately decoupled from the SPI types: it accepts
// any Worker that satisfies the narrow `Run(ctx) error` contract. The
// per-sub-component supervisors (P1.1 in docs/future-work.md §1.1) wire
// their concrete worker into a Supervisor instance; the surrounding host
// glue is added by a separate story when the four supervisors are wired
// into LifecycleCoordinator.
//
// Concurrency model:
//   - One Worker per Supervisor.
//   - Start runs the worker on the calling goroutine; callers typically
//     `go sv.Start(ctx)`.
//   - Stop signals shutdown and blocks until the worker has exited or the
//     supplied stop-context's deadline is reached.
//   - Panic in Run is recovered and counted as one failure; backoff then
//     restart, exactly as for a returned error.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// State enumerates the supervisor's coarse lifecycle state. Snapshots are
// returned by Status() and passed to OnHealthTick.
type State int

const (
	// StateIdle is the state before Start has been called.
	StateIdle State = iota
	// StateRunning means the worker's Run is executing.
	StateRunning
	// StateRestarting means the worker has exited and the supervisor is
	// sleeping the backoff before the next Run.
	StateRestarting
	// StateStopped means Stop has completed.
	StateStopped
)

// String returns a stable label suitable for metrics.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateRestarting:
		return "restarting"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Worker is the contract a supervised component implements. Run blocks
// until ctx is canceled or an unrecoverable error is returned. A panic is
// recovered by the supervisor and treated as an error.
type Worker interface {
	Run(ctx context.Context) error
}

// Status is a point-in-time snapshot of supervisor state. It is safe to
// share across goroutines; the supervisor builds a fresh value on every
// Status() call.
type Status struct {
	AdapterID    string
	State        State
	RestartCount int64
	LastError    error
	LastErrorAt  time.Time
	Stopped      bool
}

// Options configures New.
type Options struct {
	// AdapterID is stamped into every Status snapshot so metrics emitters
	// can label series. Required-recommended; an empty string is allowed
	// for tests but production wiring should always set this.
	AdapterID string

	// Worker is the supervised component. Required.
	Worker Worker

	// BackoffInitial is the first sleep after a Run failure. Required and
	// must be > 0.
	BackoffInitial time.Duration

	// BackoffMax caps the exponential backoff. Required and must be >=
	// BackoffInitial.
	BackoffMax time.Duration

	// JitterMax is the maximum proportional jitter added to each sleep,
	// in [0, 1]. The actual sleep is `base * (1 + jitter() * JitterMax)`.
	// Default 0.2 (+20%).
	JitterMax float64

	// Jitter returns a value in [0, 1]; defaults to math/rand.Float64.
	// Tests pin this to 0 for determinism.
	Jitter func() float64

	// Sleep is the time-source used to wait out backoff. Default sleeps
	// against time.NewTimer + ctx. Tests stub this to record durations.
	// The implementation MUST honor ctx cancellation.
	Sleep func(ctx context.Context, d time.Duration)

	// HealthInterval is the cadence at which OnHealthTick fires. Zero
	// disables ticks (useful for tests that don't observe health).
	HealthInterval time.Duration

	// OnHealthTick fires every HealthInterval with the current Status.
	// Production wiring publishes this to the metrics registry. Optional.
	OnHealthTick func(Status)
}

// ErrStopTimeout is returned by Stop when the stop-context's deadline
// expires before the worker exits. Callers can errors.Is against this
// to distinguish a hung worker from a clean shutdown.
var ErrStopTimeout = errors.New("supervisor: stop timed out before worker exited")

// Supervisor wraps one Worker and owns its lifecycle. Construct via New.
type Supervisor struct {
	adapterID      string
	worker         Worker
	backoffInitial time.Duration
	backoffMax     time.Duration
	jitterMax      float64
	jitter         func() float64
	sleep          func(ctx context.Context, d time.Duration)
	healthInterval time.Duration
	onHealthTick   func(Status)

	// state is the lifecycle state, accessed under mu except via
	// atomic loads in Status() for cheap snapshotting.
	mu           sync.Mutex
	state        atomic.Int32 // State
	restartCount atomic.Int64
	lastErr      atomic.Pointer[failure]
	stopped      atomic.Bool

	// stopReq is closed by Stop to request shutdown. cancelStart cancels
	// the Start ctx so the in-flight Run wakes up.
	stopReq     chan struct{}
	stopOnce    sync.Once
	cancelStart context.CancelFunc

	// done is closed when Start returns.
	done chan struct{}
}

type failure struct {
	err error
	at  time.Time
}

// New constructs a Supervisor. Returns an error if Options are invalid.
func New(opts Options) (*Supervisor, error) {
	if opts.Worker == nil {
		return nil, errors.New("supervisor: Worker is required")
	}
	if opts.BackoffInitial <= 0 {
		return nil, errors.New("supervisor: BackoffInitial must be > 0")
	}
	if opts.BackoffMax < opts.BackoffInitial {
		return nil, errors.New("supervisor: BackoffMax must be >= BackoffInitial")
	}
	if opts.JitterMax < 0 || opts.JitterMax > 1 {
		return nil, errors.New("supervisor: JitterMax must be in [0,1]")
	}
	jitterMax := opts.JitterMax
	if jitterMax == 0 {
		jitterMax = 0.2
	}
	jitter := opts.Jitter
	if jitter == nil {
		// Per-supervisor rand source so concurrent supervisors don't
		// contend on the global lock and don't share state. Backoff
		// jitter is not security-relevant; PCG is intentional.
		//nolint:gosec // G115: UnixNano is monotonic positive; truncation is intentional.
		seed := uint64(time.Now().UnixNano())
		//nolint:gosec // G404: jitter RNG is non-security; PCG is fine.
		r := rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
		jitter = r.Float64
	}
	sleep := opts.Sleep
	if sleep == nil {
		sleep = defaultSleep
	}
	sv := &Supervisor{
		adapterID:      opts.AdapterID,
		worker:         opts.Worker,
		backoffInitial: opts.BackoffInitial,
		backoffMax:     opts.BackoffMax,
		jitterMax:      jitterMax,
		jitter:         jitter,
		sleep:          sleep,
		healthInterval: opts.HealthInterval,
		onHealthTick:   opts.OnHealthTick,
		stopReq:        make(chan struct{}),
		done:           make(chan struct{}),
	}
	sv.state.Store(int32(StateIdle))
	return sv, nil
}

// Start runs the supervised worker. It blocks until ctx or Stop fires,
// recovering panics, restarting on every Run exit (error, panic, or nil
// without ctx-cancellation), applying exponential backoff with jitter
// between restarts.
//
// Start always returns nil; the per-restart errors are observable via
// Status().LastError. A non-nil return is reserved for future fatal
// conditions (e.g., a permanent-error sentinel from the worker).
func (s *Supervisor) Start(ctx context.Context) error {
	defer close(s.done)
	defer s.state.Store(int32(StateStopped))

	// Wrap the parent ctx so Stop() can cancel it independently.
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancelStart = cancel
	s.mu.Unlock()
	defer cancel()

	// Health tick lifecycle is tied to Start's lifetime.
	var healthDone chan struct{}
	if s.healthInterval > 0 && s.onHealthTick != nil {
		healthDone = make(chan struct{})
		go s.runHealthTicks(runCtx, healthDone)
		defer func() { <-healthDone }()
	}

	backoff := s.backoffInitial
	for {
		// Honor stop / ctx between iterations.
		if s.shouldExit(runCtx) {
			return nil
		}

		s.state.Store(int32(StateRunning))
		err := s.runOnce(runCtx)

		// Clean exit driven by ctx cancellation: don't restart, don't
		// count this as a failure.
		if err == nil && runCtx.Err() != nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if runCtx.Err() != nil {
				return nil
			}
		}

		// Record the failure (error or implicit nil-without-cancel).
		if err == nil {
			err = errors.New("supervisor: worker returned without context cancellation")
		}
		s.recordFailure(err)

		if s.shouldExit(runCtx) {
			return nil
		}

		// Sleep the backoff, then escalate.
		s.state.Store(int32(StateRestarting))
		actual := s.applyJitter(backoff)
		s.sleep(runCtx, actual)
		backoff = s.nextBackoff(backoff)
	}
}

// runOnce executes Worker.Run with panic recovery. A recovered panic
// becomes an error so the outer loop's restart accounting is uniform.
func (s *Supervisor) runOnce(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("supervisor: worker panic: %v", r)
		}
	}()
	return s.worker.Run(ctx)
}

func (s *Supervisor) recordFailure(err error) {
	s.restartCount.Add(1)
	s.lastErr.Store(&failure{err: err, at: time.Now()})
}

// nextBackoff doubles up to backoffMax. Guards against overflow on long
// outages by clamping any non-positive or overshoot value to backoffMax.
func (s *Supervisor) nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next <= 0 || next > s.backoffMax {
		return s.backoffMax
	}
	return next
}

func (s *Supervisor) applyJitter(base time.Duration) time.Duration {
	if s.jitterMax == 0 {
		return base
	}
	mult := 1 + s.jitter()*s.jitterMax
	out := time.Duration(float64(base) * mult)
	if out <= 0 {
		return base
	}
	return out
}

func (s *Supervisor) shouldExit(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	select {
	case <-s.stopReq:
		return true
	default:
		return false
	}
}

// Stop signals the worker to shut down and blocks until Run has exited or
// the supplied ctx's deadline expires. Returns ErrStopTimeout on deadline.
//
// Stop is idempotent: subsequent calls return immediately with whatever
// the first call resolved to.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.stopOnce.Do(func() {
		close(s.stopReq)
		s.mu.Lock()
		c := s.cancelStart
		s.mu.Unlock()
		if c != nil {
			c()
		}
	})
	select {
	case <-s.done:
		s.stopped.Store(true)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrStopTimeout, ctx.Err())
	}
}

// Status returns a snapshot of the supervisor's current state. Cheap;
// safe to call from a metrics scrape goroutine.
func (s *Supervisor) Status() Status {
	st := Status{
		AdapterID:    s.adapterID,
		State:        State(s.state.Load()),
		RestartCount: s.restartCount.Load(),
		Stopped:      s.stopped.Load(),
	}
	if f := s.lastErr.Load(); f != nil {
		st.LastError = f.err
		st.LastErrorAt = f.at
	}
	return st
}

func (s *Supervisor) runHealthTicks(ctx context.Context, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(s.healthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopReq:
			return
		case <-t.C:
			s.onHealthTick(s.Status())
		}
	}
}

// defaultSleep is the production sleep: respects ctx cancellation so a
// hung supervisor in backoff still wakes up on Stop.
func defaultSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
