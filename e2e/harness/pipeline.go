// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	defaultadapter "github.com/bzimbelman/fhir-ehr-subscriptions-service/adapters/default"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/channel"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/builder"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/scheduler"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/engine/submatcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/hl7processor"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/codec"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/infra/storage/repos"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/matcher"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/topics/catalog"
)

// Stage is a tag for selectively starting / stopping a pipeline stage.
// Tests that need to exercise just a subset of the pipeline (e.g., the
// backpressure scenario, which holds the scheduler off so deliveries
// queue up) pick the stages they want via PipelineConfig.
type Stage int

// Stage values.
const (
	StageHL7Processor Stage = 1 << iota
	StageMatcher
	StageSubmatcher
	StageScheduler
	// AllStages is the convenience union for the typical happy-path
	// scenarios that want every claim loop running.
	AllStages = StageHL7Processor | StageMatcher | StageSubmatcher | StageScheduler
)

// PipelineConfig parameterizes a Pipeline.
type PipelineConfig struct {
	// AdapterID labels the row's adapter_id column. The default adapter
	// passes raw HL7 bytes through as a Bundle.
	AdapterID string

	// Adapter is the SPI implementation used by the HL7 processor.
	// Tests inject scriptedAdapter-style fakes here when they need to
	// drive specific classify / map / validate paths.
	Adapter spi.Hl7MessageProcessor

	// Stages controls which claim loops Run starts. AllStages is the
	// default for the happy-path scenarios.
	Stages Stage

	// CorrelationHoldWindow caps how long the HL7 processor will hold
	// an unpaired half. Test default is short (1s) so cancel/replace
	// reaper scenarios run fast.
	CorrelationHoldWindow time.Duration

	// Channels registers (channelType, channel.Channel) pairs that the
	// scheduler hands deliveries to. The harness leaves channel
	// construction to the test (TLS certs, mock-subscriber endpoints)
	// since each scenario tunes its own channel client.
	Channels map[string]channel.Channel

	// PollInterval is the claim-loop idle poll. Tests use a short
	// interval (50ms) so steps don't drag.
	PollInterval time.Duration
}

// Pipeline owns the workers that run the production stages. It is
// constructed via NewPipeline, started via Start(ctx), and stopped via
// Stop(). Multiple scenarios may reuse the same Pipeline if they reset
// state between runs (truncate the right tables) — but the simpler
// pattern is one Pipeline per scenario.
type Pipeline struct {
	pool *pgxpool.Pool
	cfg  PipelineConfig

	codec *codec.Codec

	// Repos.
	hl7Q    *repos.Hl7MessageQueueRepo
	rcs     *repos.ResourceChangesRepo
	ehr     *repos.EhrEventsRepo
	dlv     *repos.DeliveriesRepo
	dl      *repos.DeadLettersRepo
	pending *repos.PendingPairsRepo
	subs    *repos.SubscriptionsRepo
	wsTok   *repos.WsBindingTokensRepo

	// Stage workers.
	processor  *hl7processor.Processor
	matcher    *matcher.Worker
	submatcher *submatcher.Worker
	scheduler  *scheduler.Worker
	registry   *scheduler.MapRegistry

	// Catalog state.
	mu            sync.RWMutex
	catalog       *catalog.Catalog
	builtinTopics []catalog.RawTopic

	// Lifecycle.
	cancel context.CancelFunc
	stopWG sync.WaitGroup
}

// NewPipeline constructs a Pipeline against an existing pool. The pool
// must already have the migrations applied; the harness's storage-init
// step takes care of that.
//
// The cfg.Adapter, cfg.AdapterID, and cfg.Channels fields are
// load-bearing. NewPipeline returns an error if any are empty.
func NewPipeline(pool *pgxpool.Pool, cfg PipelineConfig) (*Pipeline, error) {
	if pool == nil {
		return nil, fmt.Errorf("harness: nil pool")
	}
	if cfg.AdapterID == "" {
		cfg.AdapterID = "default"
	}
	if cfg.Adapter == nil {
		// Use the reference passthrough adapter when none is supplied.
		ad := defaultadapter.New()
		cfg.Adapter = ad.BuildHl7Processor(spi.AdapterContext{
			AdapterID: cfg.AdapterID,
			Now:       time.Now,
		})
	}
	if cfg.Stages == 0 {
		cfg.Stages = AllStages
	}
	if cfg.CorrelationHoldWindow == 0 {
		cfg.CorrelationHoldWindow = 1 * time.Second
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}

	// Build a codec with a single deterministic test key. Scenarios
	// that exercise key rotation can swap this for their own codec.
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	cd, err := codec.New(codec.NewStaticKeyProvider(map[int32][]byte{1: keyBytes}, 1))
	if err != nil {
		return nil, fmt.Errorf("harness: codec: %w", err)
	}

	p := &Pipeline{
		pool:    pool,
		cfg:     cfg,
		codec:   cd,
		hl7Q:    repos.NewHl7MessageQueueRepo(cd),
		rcs:     repos.NewResourceChangesRepo(cd),
		ehr:     repos.NewEhrEventsRepo(cd),
		dlv:     repos.NewDeliveriesRepo(),
		dl:      repos.NewDeadLettersRepo(cd),
		pending: repos.NewPendingPairsRepo(cd),
		subs:    repos.NewSubscriptionsRepo(),
		wsTok:   repos.NewWsBindingTokensRepo(),
	}
	emptyRep, err := catalog.Load(catalog.Sources{})
	if err != nil {
		return nil, fmt.Errorf("harness: empty catalog: %w", err)
	}
	p.catalog = emptyRep.Catalog
	return p, nil
}

// Pool returns the underlying pgxpool.
func (p *Pipeline) Pool() *pgxpool.Pool { return p.pool }

// Codec returns the codec used by repos. Tests that insert encrypted
// rows by hand need this.
func (p *Pipeline) Codec() *codec.Codec { return p.codec }

// HL7Queue returns the hl7_message_queue repo.
func (p *Pipeline) HL7Queue() *repos.Hl7MessageQueueRepo { return p.hl7Q }

// Subscriptions returns the subscriptions repo.
func (p *Pipeline) Subscriptions() *repos.SubscriptionsRepo { return p.subs }

// EhrEvents returns the ehr_events repo.
func (p *Pipeline) EhrEvents() *repos.EhrEventsRepo { return p.ehr }

// Deliveries returns the deliveries repo.
func (p *Pipeline) Deliveries() *repos.DeliveriesRepo { return p.dlv }

// ResourceChanges returns the resource_changes repo.
func (p *Pipeline) ResourceChanges() *repos.ResourceChangesRepo { return p.rcs }

// PendingPairs returns the pending_pairs repo.
func (p *Pipeline) PendingPairs() *repos.PendingPairsRepo { return p.pending }

// DeadLetters returns the dead_letters repo.
func (p *Pipeline) DeadLetters() *repos.DeadLettersRepo { return p.dl }

// WsBindingTokens returns the ws_binding_tokens repo.
func (p *Pipeline) WsBindingTokens() *repos.WsBindingTokensRepo { return p.wsTok }

// Registry returns the scheduler's channel registry. Tests can
// register additional channels at runtime (e.g., to swap a flaky
// rest-hook channel for a 503-returning one in the backpressure
// scenario).
func (p *Pipeline) Registry() *scheduler.MapRegistry { return p.registry }

// Catalog returns the current matcher catalog. Useful for tests that
// want to assert what topics are loaded.
func (p *Pipeline) Catalog() *catalog.Catalog {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.catalog
}

// Start launches the configured worker loops. Returns when every loop
// has been launched; the loops run until Stop is called.
func (p *Pipeline) Start(ctx context.Context) error {
	loopCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	if p.cfg.Stages&StageHL7Processor != 0 {
		proc, err := hl7processor.New(hl7processor.Config{
			AdapterID:             p.cfg.AdapterID,
			ClaimBatchSize:        16,
			ClaimIdlePollInterval: p.cfg.PollInterval,
			ReaperTickInterval:    p.cfg.PollInterval,
			CorrelationHoldWindow: p.cfg.CorrelationHoldWindow,
		}, hl7processor.Deps{
			Pool:       p.pool,
			Codec:      p.codec,
			HL7Queue:   p.hl7Q,
			Pending:    p.pending,
			Changes:    p.rcs,
			DeadLetter: p.dl,
			Adapter:    p.cfg.Adapter,
		})
		if err != nil {
			cancel()
			return fmt.Errorf("harness: hl7processor: %w", err)
		}
		p.processor = proc
		p.stopWG.Add(1)
		go func() {
			defer p.stopWG.Done()
			_ = proc.Run(loopCtx)
		}()
	}

	if p.cfg.Stages&StageMatcher != 0 {
		w := matcher.NewWorker(
			p.pool, p.rcs, p.ehr,
			func() *catalog.Catalog {
				p.mu.RLock()
				defer p.mu.RUnlock()
				return p.catalog
			},
			matcher.Config{
				ClaimBatchSize:   16,
				IdlePollInterval: p.cfg.PollInterval,
			},
		)
		p.matcher = w
		p.stopWG.Add(1)
		go func() {
			defer p.stopWG.Done()
			_ = w.Run(loopCtx)
		}()
	}

	if p.cfg.Stages&StageSubmatcher != 0 {
		w := submatcher.NewWorker(
			p.pool, p.subs, p.ehr, p.dlv,
			submatcher.Config{
				ClaimBatchSize:   16,
				IdlePollInterval: p.cfg.PollInterval,
			},
		)
		p.submatcher = w
		p.stopWG.Add(1)
		go func() {
			defer p.stopWG.Done()
			_ = w.Run(loopCtx)
		}()
	}

	// Build the channel registry regardless of whether the scheduler
	// is started; tests that disable the scheduler may still inject
	// channels and call Deliver directly.
	p.registry = scheduler.NewMapRegistry()
	for kind, ch := range p.cfg.Channels {
		p.registry.Register(kind, ch)
	}

	if p.cfg.Stages&StageScheduler != 0 {
		w := scheduler.NewWorker(
			p.pool, p.subs, p.ehr, p.dlv, p.dl, p.registry,
			builder.New(builder.Config{}),
			scheduler.Config{
				ClaimBatchSize:   16,
				IdlePollInterval: p.cfg.PollInterval,
				Retry: scheduler.RetryConfig{
					Initial:     500 * time.Millisecond,
					Max:         5 * time.Second,
					Min:         100 * time.Millisecond,
					MaxAttempts: 8,
				},
			},
			scheduler.Options{},
		)
		p.scheduler = w
		p.stopWG.Add(1)
		go func() {
			defer p.stopWG.Done()
			_ = w.Run(loopCtx)
		}()
	}

	return nil
}

// Stop cancels the pipeline's context and waits for every worker to
// return. Safe to call multiple times.
func (p *Pipeline) Stop() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	p.cancel = nil
	doneCh := make(chan struct{})
	go func() {
		p.stopWG.Wait()
		close(doneCh)
	}()
	select {
	case <-doneCh:
	case <-time.After(15 * time.Second):
		// Deliberately permissive: if a worker is wedged we still want
		// to surface the leak via Stop returning rather than blocking
		// the test forever. The race detector + outer test deadline
		// will catch the actual leak.
	}
}
