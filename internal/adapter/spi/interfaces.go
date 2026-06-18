// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package spi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// AdapterContext is the dependency bundle the host injects into every
// EhrAdapter.Build* call. The adapter does NOT receive a database connection.
// All durable writes go through ResourceChangeSink, which the framework wires
// to the transactional outbox; the adapter persists its own working state
// through StateStore.
type AdapterContext struct {
	// Config is the adapter's validated `adapter.config` block from the host
	// configuration, post-secret-resolution.
	Config []byte

	// StateStore is the per-adapter scoped KV store. Keys are namespaced under
	// the adapter id by the host; an adapter cannot read or write another
	// adapter's state.
	StateStore AdapterStateStore

	// HTTP is the pre-configured HTTP client (auth, TLS, retries, user-agent).
	// Set as opaque; concrete adapters may type-assert to a richer client at
	// build time, but the SPI deliberately keeps the surface narrow so a
	// future framework can swap in a sandboxed implementation without an SPI
	// break.
	HTTP HTTPDoer

	// ResourceChangeSink is the framework-internal sink the supervisors call
	// after the vendor's translate / map_to_fhir step. Adapters never call it
	// directly; they return ResourceChange values from their REQUIRED methods
	// and the supervisor invokes the sink.
	ResourceChangeSink ResourceChangeSink

	// Now returns the current time. Injected so tests can stub the clock.
	Now func() time.Time

	// AdapterID is the manifest id; pre-applied to logger and metric labels
	// by the host but exposed here for adapters that need it directly.
	AdapterID string
}

// HTTPDoer is the minimal HTTP surface the SPI exposes. Concrete framework
// implementations will provide a struct with auth and retry; the SPI keeps
// only what an adapter cannot avoid.
type HTTPDoer interface {
	Do(req any) (any, error)
}

// AdapterStateStore is the per-adapter scoped KV the host injects.
type AdapterStateStore interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]StateEntry, error)
}

// StateEntry is one (key, value) pair returned from List.
type StateEntry struct {
	Key   string
	Value []byte
}

// ResourceChangeSink is the framework-internal write surface used by
// supervisors. Adapters do not call this directly; the SPI exposes it so the
// AdapterContext shape is complete and so test doubles can substitute it.
type ResourceChangeSink interface {
	Write(ctx context.Context, change ResourceChange) error
}

// ParsedHL7Message is the typed segment tree produced by Hl7MessageProcessor.Lex.
// The SPI keeps the shape opaque to the host: the vendor's lex output is its
// own structure; the host only consumes Classify and MapToFHIR results.
type ParsedHL7Message struct {
	// Raw is the original bytes the lex consumed (carried through for debug
	// and dead-letter payload).
	Raw []byte

	// Segments is a vendor-defined opaque carrier. Vendors that need typed
	// access cast to their own concrete type. The SPI does not constrain the
	// shape because vendors extend the parser with their own Z-segment trees.
	Segments any
}

// Classification is the result of Hl7MessageProcessor.Classify per LLD.
// CorrelationKey is the vendor-specific identifier the framework uses to
// pair cancel-and-replace within the configured hold window.
type Classification struct {
	Kind           ChangeKind
	CorrelationKey string
}

// Hl7MessageProcessor is the Stage 1 source for HL7 v2 messages. Vendor
// adapters provide implementations by embedding BaseHl7MessageProcessor and
// overriding the REQUIRED methods.
type Hl7MessageProcessor interface {
	Lex(raw []byte) (ParsedHL7Message, error)
	Classify(parsed ParsedHL7Message) (Classification, error)
	MapToFHIR(parsed ParsedHL7Message, c Classification) (FhirResource, error)
	Validate(resource FhirResource) error
	CorrelationHoldWindow() time.Duration
	OnUnpairedCancellation(resource FhirResource, correlationID uuid.UUID, occurredAt time.Time) ResourceChange
	OnUnpairedReplacement(resource FhirResource, correlationID uuid.UUID, occurredAt time.Time) ResourceChange
}

// BaseHl7MessageProcessor provides default implementations of the OPTIONAL
// Hl7MessageProcessor methods. Embed and override REQUIRED methods.
type BaseHl7MessageProcessor struct{}

// Lex panics — REQUIRED override.
func (BaseHl7MessageProcessor) Lex(_ []byte) (ParsedHL7Message, error) {
	panic("spi: Hl7MessageProcessor.Lex must be overridden by the vendor adapter")
}

// Classify panics — REQUIRED override.
func (BaseHl7MessageProcessor) Classify(_ ParsedHL7Message) (Classification, error) {
	panic("spi: Hl7MessageProcessor.Classify must be overridden by the vendor adapter")
}

// MapToFHIR panics — REQUIRED override.
func (BaseHl7MessageProcessor) MapToFHIR(_ ParsedHL7Message, _ Classification) (FhirResource, error) {
	panic("spi: Hl7MessageProcessor.MapToFHIR must be overridden by the vendor adapter")
}

// Validate is the OPTIONAL post-mapping validation hook. The v1 default is
// permissive; per ADR 0010 a stricter validator is swapped in via the host
// configuration.
func (BaseHl7MessageProcessor) Validate(_ FhirResource) error { return nil }

// CorrelationHoldWindow is the cancel-and-replace pairing window. Default 30s
// per architecture and contracts/adapter-spi.md.
func (BaseHl7MessageProcessor) CorrelationHoldWindow() time.Duration {
	return 30 * time.Second
}

// OnUnpairedCancellation emits a plain delete when the hold window expires
// without a matching replacement.
func (BaseHl7MessageProcessor) OnUnpairedCancellation(r FhirResource, correlationID uuid.UUID, occurredAt time.Time) ResourceChange {
	return ResourceChange{
		ResourceType:  r.ResourceType,
		ChangeKind:    ChangeDelete,
		Resource:      r,
		OccurredAt:    occurredAt,
		CorrelationID: correlationID,
	}
}

// OnUnpairedReplacement emits a plain create when a replacement arrives with
// no preceding cancellation in the hold window.
func (BaseHl7MessageProcessor) OnUnpairedReplacement(r FhirResource, correlationID uuid.UUID, occurredAt time.Time) ResourceChange {
	return ResourceChange{
		ResourceType:  r.ResourceType,
		ChangeKind:    ChangeCreate,
		Resource:      r,
		OccurredAt:    occurredAt,
		CorrelationID: correlationID,
	}
}

// ScanTarget is one entry in a FhirScanRunner.ScanPlan.
type ScanTarget struct {
	ResourceType string
	Cadence      time.Duration
	QueryParams  map[string]string
}

// ScanIterator yields one FhirResource per call to Next. ok=false signals the
// iterator is exhausted; an error terminates the scan and routes through the
// supervisor's retry/backoff.
type ScanIterator interface {
	Next(ctx context.Context) (FhirResource, bool, error)
}

// FhirScanRunner is the Stage 1 source for periodic FHIR scans. Embed
// BaseFhirScanRunner for default ContentHash and Normalize; override
// REQUIRED ScanPlan and RunScan.
type FhirScanRunner interface {
	ScanPlan() []ScanTarget
	RunScan(ctx context.Context, target ScanTarget) (ScanIterator, error)
	ContentHash(resource FhirResource) string
	Normalize(resource FhirResource) FhirResource
}

// BaseFhirScanRunner provides default implementations of the OPTIONAL
// FhirScanRunner methods.
type BaseFhirScanRunner struct{}

// ScanPlan panics — REQUIRED override.
func (BaseFhirScanRunner) ScanPlan() []ScanTarget {
	panic("spi: FhirScanRunner.ScanPlan must be overridden by the vendor adapter")
}

// RunScan panics — REQUIRED override.
func (BaseFhirScanRunner) RunScan(_ context.Context, _ ScanTarget) (ScanIterator, error) {
	panic("spi: FhirScanRunner.RunScan must be overridden by the vendor adapter")
}

// ContentHash returns a hex-encoded SHA-256 of the resource body. Vendors
// override to apply canonicalization (e.g., RFC 8785 JCS per ADR 0010) or
// strip vendor-volatile fields before hashing.
func (BaseFhirScanRunner) ContentHash(r FhirResource) string {
	sum := sha256.Sum256(r.Body)
	return hex.EncodeToString(sum[:])
}

// Normalize is the identity by default. Override for profile normalization.
func (BaseFhirScanRunner) Normalize(r FhirResource) FhirResource { return r }

// EventSink receives vendor records from a VendorAPIClient.Consume and hands
// them to the supervisor for translation + outbox write. The framework
// implements the sink; vendors call Push from their consume loop.
type EventSink interface {
	Push(ctx context.Context, record VendorRecord) error
}

// VendorRecord is one record from a vendor change-feed. The SPI keeps the
// shape opaque; the vendor's Translate is the only thing that interprets it.
type VendorRecord struct {
	// Cursor is the vendor-specific cursor advance the supervisor persists
	// after the resulting ResourceChange row commits.
	Cursor []byte

	// Payload is the vendor-defined record body.
	Payload any

	// EventCode is an optional event code stamped onto the resulting
	// ResourceChange.event_code for SubscriptionTopic.eventTrigger matching.
	EventCode string
}

// VendorAPIClient is the Stage 1 source for vendor proprietary APIs and
// change feeds. Has no Base because there are no OPTIONAL methods.
type VendorAPIClient interface {
	Consume(ctx context.Context, sink EventSink, cursor []byte) error
	Translate(record VendorRecord) (ResourceChange, error)
}

// FhirReference is the resolved reference type a HydrationService.Fetch
// receives from the engine. Type and ID are split so vendor implementations
// can map directly to their FHIR REST URL builders.
type FhirReference struct {
	ResourceType string
	ID           string
}

// HydrationService serves on-demand resource fetches the engine asks for when
// building full-resource notification bundles. Embed BaseHydrationService for
// the default CacheTTL.
type HydrationService interface {
	Fetch(ctx context.Context, ref FhirReference) (FhirResource, error)
	CacheTTL() time.Duration
}

// BaseHydrationService provides default OPTIONAL methods.
type BaseHydrationService struct{}

// Fetch panics — REQUIRED override.
func (BaseHydrationService) Fetch(_ context.Context, _ FhirReference) (FhirResource, error) {
	panic("spi: HydrationService.Fetch must be overridden by the vendor adapter")
}

// CacheTTL is 60s by default per architecture.
func (BaseHydrationService) CacheTTL() time.Duration { return 60 * time.Second }

// EhrAdapter is the top-level interface a vendor adapter implements. Embed
// BaseEhrAdapter for default OnStart / OnShutdown no-ops.
type EhrAdapter interface {
	Manifest() AdapterManifest
	BuildHl7Processor(ctx AdapterContext) Hl7MessageProcessor
	BuildFhirScanRunner(ctx AdapterContext) FhirScanRunner
	BuildVendorAPIClient(ctx AdapterContext) VendorAPIClient
	BuildHydrationService(ctx AdapterContext) HydrationService
	OnStart(ctx context.Context, actx AdapterContext) error
	OnShutdown(ctx context.Context, actx AdapterContext) error
}

// BaseEhrAdapter provides no-op OnStart and OnShutdown defaults. Embed and
// override the REQUIRED Manifest + Build* methods.
type BaseEhrAdapter struct{}

// Manifest panics — REQUIRED override.
func (BaseEhrAdapter) Manifest() AdapterManifest {
	panic("spi: EhrAdapter.Manifest must be overridden by the vendor adapter")
}

// BuildHl7Processor panics — REQUIRED override.
func (BaseEhrAdapter) BuildHl7Processor(_ AdapterContext) Hl7MessageProcessor {
	panic("spi: EhrAdapter.BuildHl7Processor must be overridden by the vendor adapter")
}

// BuildFhirScanRunner panics — REQUIRED override.
func (BaseEhrAdapter) BuildFhirScanRunner(_ AdapterContext) FhirScanRunner {
	panic("spi: EhrAdapter.BuildFhirScanRunner must be overridden by the vendor adapter")
}

// BuildVendorAPIClient panics — REQUIRED override.
func (BaseEhrAdapter) BuildVendorAPIClient(_ AdapterContext) VendorAPIClient {
	panic("spi: EhrAdapter.BuildVendorAPIClient must be overridden by the vendor adapter")
}

// BuildHydrationService panics — REQUIRED override.
func (BaseEhrAdapter) BuildHydrationService(_ AdapterContext) HydrationService {
	panic("spi: EhrAdapter.BuildHydrationService must be overridden by the vendor adapter")
}

// OnStart is the OPTIONAL adapter-wide setup hook. Default no-op.
func (BaseEhrAdapter) OnStart(_ context.Context, _ AdapterContext) error { return nil }

// OnShutdown is the OPTIONAL adapter-wide teardown hook. Default no-op.
func (BaseEhrAdapter) OnShutdown(_ context.Context, _ AdapterContext) error { return nil }
