// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/supervisor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/lifecycle"
)

// PipelineSupervisorConfig is the operator-tunable bundle for the
// adapter Supervisor framework that hosts the four pipeline workers.
// Defaults match the previous bare-goroutine cadence (no sleep at all)
// scaled to a sane production starting point: 100ms initial, 30s max.
//
// HealthInterval=0 disables OnHealthTick (useful when no metrics layer
// is wired). When non-zero each supervisor publishes its Status to the
// host-supplied OnHealthTick callback at the configured cadence.
type PipelineSupervisorConfig struct {
	// BackoffInitial is the first sleep after a panic / Run-error
	// before the supervisor restarts the worker. Default 100ms.
	BackoffInitial time.Duration `yaml:"backoff_initial"`

	// BackoffMax caps exponential backoff. Default 30s.
	BackoffMax time.Duration `yaml:"backoff_max"`

	// JitterMax bounds the proportional jitter added to each sleep,
	// in [0, 1]. Default 0.2 (+20%).
	JitterMax float64 `yaml:"jitter_max"`

	// HealthInterval is the cadence at which OnHealthTick fires.
	// Default 30s; zero disables ticks.
	HealthInterval time.Duration `yaml:"health_interval"`

	// StopGrace is the per-supervisor budget the drain hook gives a
	// hung worker before returning a deadline error. Default 5s.
	StopGrace time.Duration `yaml:"stop_grace"`
}

// applyDefaults fills in any zero field with the production default.
func (c *PipelineSupervisorConfig) applyDefaults() {
	if c.BackoffInitial <= 0 {
		c.BackoffInitial = 100 * time.Millisecond
	}
	if c.BackoffMax < c.BackoffInitial {
		c.BackoffMax = 30 * time.Second
	}
	if c.JitterMax <= 0 {
		c.JitterMax = 0.2
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 30 * time.Second
	}
	if c.StopGrace <= 0 {
		c.StopGrace = 5 * time.Second
	}
}

// supervisedPipeline is the host-side handle returned by
// buildSupervisedPipeline. It owns the goroutines for every supervised
// pipeline worker. Callers shut the whole pipeline down via Stop; the
// drain hook the helper registers on the lifecycle module calls Stop
// during PhaseDrainInFlight so a SIGTERM also drains cleanly.
type supervisedPipeline struct {
	supervisors map[string]*supervisorEntry

	// orderedIDs preserves a deterministic iteration order for Status()
	// so tests and the admin endpoint surface a stable layout.
	orderedIDs []string

	// backoff is the resolved supervisor tunable bundle Restart re-uses
	// when re-creating a supervisor. Keeping it on the pipeline avoids
	// hardcoding a different (hidden) backoff for the operator-driven
	// restart path.
	backoff PipelineSupervisorConfig

	// onHealth is the host-supplied health-tick callback Restart
	// re-attaches to a re-created supervisor.
	onHealth func(name string, st supervisor.Status)

	stopOnce sync.Once
	stopErr  error
}

type supervisorEntry struct {
	id       string
	sv       *supervisor.Supervisor
	worker   supervisor.Worker
	startCtx context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// pipelineSupervisorDeps bundles the inputs to buildSupervisedPipeline.
// Each Worker in turn satisfies internal/adapter/supervisor.Worker
// (Run(ctx) error). The pipeline workers in production
// (hl7processor.Processor, matcher.Worker, submatcher.Worker,
// scheduler.Worker) all match this contract today.
type pipelineSupervisorDeps struct {
	HL7        supervisor.Worker
	Matcher    supervisor.Worker
	Submatcher supervisor.Worker
	Scheduler  supervisor.Worker

	// FhirScanRunner is the optional periodic FHIR scan worker (story
	// #96). Wired only when the loaded adapter declares
	// Capabilities.FhirScanRunner=true; nil otherwise. Hosted under
	// supervisor id "fhir-scan-runner" so it shows up alongside the
	// other pipeline workers in /admin/supervisor/status.
	FhirScanRunner supervisor.Worker

	// VendorAPIClient is the optional vendor change-feed worker (story
	// #97). Wired only when the loaded adapter declares
	// Capabilities.VendorAPIClient=true; nil otherwise. Hosted under
	// supervisor id "vendor-api-client" so it shows up alongside the
	// other pipeline workers in /admin/supervisor/status.
	VendorAPIClient supervisor.Worker

	// Lifecycle is the lifecycle module the helper registers its
	// shutdown hook against. Required.
	Lifecycle *lifecycle.LifecycleModule

	// Backoff is the supervisor tunables. Defaults are applied for
	// any zero field.
	Backoff PipelineSupervisorConfig

	// OnHealth is invoked by every supervisor every Backoff.HealthInterval.
	// Production wiring publishes these to the metrics registry. Optional.
	OnHealth func(name string, st supervisor.Status)
}

// buildSupervisedPipeline constructs one supervisor.Supervisor per
// pipeline worker, kicks off the four supervisor goroutines, and
// registers a single shutdown hook in PhaseDrainInFlight that calls
// Stop on every supervisor in parallel. The returned handle exposes:
//
//   - Status: the slice the /admin/supervisor/status endpoint serializes;
//   - Restart(adapterID): operator entry point to kick a stuck worker;
//   - Stop(ctx): drains every supervisor at most ctx-bounded.
//
// Story #99: the helper exists as a single owner so the four pipeline
// loops can no longer be wired with bare `go w.Run()`. A panicking
// adapter Worker now has the supervisor watching it.
func buildSupervisedPipeline(deps pipelineSupervisorDeps) (*supervisedPipeline, error) {
	if deps.Lifecycle == nil {
		return nil, errors.New("supervisor pipeline: Lifecycle is required")
	}
	if deps.HL7 == nil || deps.Matcher == nil || deps.Submatcher == nil || deps.Scheduler == nil {
		return nil, errors.New("supervisor pipeline: every Worker (HL7, Matcher, Submatcher, Scheduler) is required")
	}
	deps.Backoff.applyDefaults()

	pl := &supervisedPipeline{
		supervisors: make(map[string]*supervisorEntry, 6),
		backoff:     deps.Backoff,
		onHealth:    deps.OnHealth,
	}

	specs := []struct {
		id     string
		worker supervisor.Worker
	}{
		{"hl7-processor", deps.HL7},
		{"matcher", deps.Matcher},
		{"submatcher", deps.Submatcher},
		{"scheduler", deps.Scheduler},
	}
	if deps.FhirScanRunner != nil {
		specs = append(specs, struct {
			id     string
			worker supervisor.Worker
		}{"fhir-scan-runner", deps.FhirScanRunner})
	}
	if deps.VendorAPIClient != nil {
		specs = append(specs, struct {
			id     string
			worker supervisor.Worker
		}{"vendor-api-client", deps.VendorAPIClient})
	}

	// OP #201: register the drain hook BEFORE launching any
	// supervisor goroutine so a SIGTERM that fires between the first
	// `go sv.Start(ctx)` and the loop's exit cannot leak workers. The
	// hook reads pl.Stop, which is idempotent and a no-op when no
	// supervisor has been registered yet, so registering before the
	// for-loop is safe even if every supervisor.New below fails.
	stopGrace := deps.Backoff.StopGrace
	deps.Lifecycle.RegisterShutdown(lifecycle.ShutdownHook{
		Name:  "pipeline.supervisors.drain",
		Phase: lifecycle.PhaseDrainInFlight,
		Run: func(ctx context.Context) error {
			drainCtx, cancel := context.WithTimeout(ctx, stopGrace)
			defer cancel()
			return pl.Stop(drainCtx)
		},
	})

	for _, sp := range specs {
		id := sp.id
		var onTick func(supervisor.Status)
		if deps.OnHealth != nil {
			onTick = func(st supervisor.Status) {
				deps.OnHealth(id, st)
			}
		}
		sv, err := supervisor.New(supervisor.Options{
			AdapterID:      id,
			Worker:         sp.worker,
			BackoffInitial: deps.Backoff.BackoffInitial,
			BackoffMax:     deps.Backoff.BackoffMax,
			JitterMax:      deps.Backoff.JitterMax,
			HealthInterval: deps.Backoff.HealthInterval,
			OnHealthTick:   onTick,
		})
		if err != nil {
			_ = pl.stopAll(context.Background())
			return nil, fmt.Errorf("supervisor %s: %w", id, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		entry := &supervisorEntry{
			id:       id,
			sv:       sv,
			worker:   sp.worker,
			startCtx: ctx,
			cancel:   cancel,
			done:     make(chan struct{}),
		}
		// Register the entry under the supervisedPipeline BEFORE
		// launching the goroutine so a concurrent Stop (driven by an
		// early SIGTERM) sees the entry in pl.supervisors and waits on
		// entry.done. Without this, a goroutine launched on a
		// supervisor that hasn't been added to the map yet leaks past
		// shutdown.
		pl.supervisors[id] = entry
		pl.orderedIDs = append(pl.orderedIDs, id)
		go func() {
			defer close(entry.done)
			_ = sv.Start(ctx)
		}()
	}

	return pl, nil
}

// Status returns one snapshot per supervisor in deterministic order.
func (p *supervisedPipeline) Status() []supervisor.Status {
	out := make([]supervisor.Status, 0, len(p.orderedIDs))
	for _, id := range p.orderedIDs {
		out = append(out, p.supervisors[id].sv.Status())
	}
	return out
}

// Restart kicks the named supervisor: cancels the in-flight Run so the
// supervisor restarts immediately (skipping the backoff sleep is the
// supervisor.Stop ctx behavior). Returns a not-found error if the id
// is unknown — operator surfaces require loud errors.
func (p *supervisedPipeline) Restart(adapterID string) error {
	entry, ok := p.supervisors[adapterID]
	if !ok {
		return fmt.Errorf("supervisor pipeline: unknown adapter id %q", adapterID)
	}
	// Cancel the underlying Start ctx — the supervisor's Run wakes up,
	// records a failure, and the outer loop restarts after the backoff
	// sleep. We then re-arm the start ctx so the supervisor keeps
	// running under a fresh cancellable parent (the prior cancel is
	// idempotent; subsequent ctx.Err() will already be set).
	entry.cancel()
	<-entry.done

	// Re-launch under a fresh ctx; the previous Supervisor instance has
	// returned. We construct a new Supervisor wrapping the same Worker
	// so Restart represents a true "host-side bounce." Reuse the
	// operator-supplied backoff bundle so Restart honors the same
	// production tunables as the initial wiring.
	var onTick func(supervisor.Status)
	if p.onHealth != nil {
		id := entry.id
		onTick = func(st supervisor.Status) { p.onHealth(id, st) }
	}
	sv, err := supervisor.New(supervisor.Options{
		AdapterID:      entry.id,
		Worker:         entry.worker,
		BackoffInitial: p.backoff.BackoffInitial,
		BackoffMax:     p.backoff.BackoffMax,
		JitterMax:      p.backoff.JitterMax,
		HealthInterval: p.backoff.HealthInterval,
		OnHealthTick:   onTick,
	})
	if err != nil {
		return fmt.Errorf("supervisor pipeline: re-create %s: %w", adapterID, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entry.sv = sv
	entry.startCtx = ctx
	entry.cancel = cancel
	entry.done = make(chan struct{})
	go func() {
		defer close(entry.done)
		_ = sv.Start(ctx)
	}()
	return nil
}

// Stop drains every supervisor. Idempotent.
func (p *supervisedPipeline) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		p.stopErr = p.stopAll(ctx)
	})
	return p.stopErr
}

func (p *supervisedPipeline) stopAll(ctx context.Context) error {
	if len(p.supervisors) == 0 {
		return nil
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, e := range p.supervisors {
		entry := e
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := entry.sv.Stop(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", entry.id, err))
				mu.Unlock()
				// Force-cancel the start ctx so the goroutine can
				// unwind even if the worker was hung past Stop's
				// deadline.
				entry.cancel()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
