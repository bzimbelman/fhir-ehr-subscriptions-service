// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/codec"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/infra/storage/repos"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Defaults per LLD §7.
const (
	DefaultClaimBatchSize        = 16
	DefaultClaimIdlePollInterval = 1 * time.Second
	DefaultReaperTickInterval    = 5 * time.Second
)

// Config tunes processor behavior.
type Config struct {
	AdapterID             string
	ClaimBatchSize        int32
	ClaimIdlePollInterval time.Duration
	ReaperTickInterval    time.Duration
	CorrelationHoldWindow time.Duration
}

// Validate reports whether cfg is usable for [New].
func (c Config) Validate() error {
	return errors.New("not implemented")
}

func (c Config) withDefaults() Config { return c }

// Deps groups host-injected dependencies.
type Deps struct {
	Pool       *pgxpool.Pool
	Codec      *codec.Codec
	HL7Queue   *repos.Hl7MessageQueueRepo
	Pending    *repos.PendingPairsRepo
	Changes    *repos.ResourceChangesRepo
	DeadLetter *repos.DeadLettersRepo
	Adapter    spi.Hl7MessageProcessor
	Metrics    MetricsEmitter
	Logger     *slog.Logger
	Now        func() time.Time
	Wakeup     <-chan struct{}
}

// Processor is the HL7 Message Processor sub-component.
type Processor struct {
	cfg  Config
	deps Deps
}

// New constructs a Processor; stub fails.
func New(_ Config, _ Deps) (*Processor, error) {
	return nil, errors.New("not implemented")
}

// Run drives the claim loop and the reaper. Stub returns error.
func (p *Processor) Run(_ context.Context) error {
	return errors.New("not implemented")
}

func (p *Processor) metrics() MetricsEmitter { return nopMetrics{} }

func pendingKindFromChange(_ spi.ChangeKind) repos.PendingKind { return "" }

func pendingKindToChange(_ repos.PendingKind) spi.ChangeKind { return "" }

func dlKindForClass(_ ErrorClass) string { return "" }

func outcomeLabel(_ outcomeKind) string { return "" }
