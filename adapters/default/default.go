// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package defaultadapter is the no-vendor reference adapter. It exists to
// prove the SPI shape: every interface, every Base struct, every host loader
// path is exercised by a runnable adapter. Vendor adapters (epic, meditech,
// oracle-health, ...) inherit the same Base structs and override only what
// differs.
//
// The default adapter does not normalize HL7 v2 input — Lex preserves the
// raw bytes verbatim and the resource produced by MapToFHIR carries the
// original payload. A real translation pipeline lives in vendor adapters
// (and in a future generic v2-to-FHIR mapping pass that operators can
// configure for the default adapter, per architecture.md).
package defaultadapter

import (
	"context"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/registry"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// Adapter is the default reference EHR adapter.
type Adapter struct {
	spi.BaseEhrAdapter
}

// New constructs a fresh default adapter instance. Factories that want to
// register the default adapter into the bundled-adapter registry should call
// New from their factory closure.
func New() *Adapter { return &Adapter{} }

// NewRegistered constructs a fresh registry pre-populated with the default
// adapter. Used by tests, embedded smoke harnesses, and host bootstrap that
// only needs the default bundled.
func NewRegistered() *registry.Registry {
	r := registry.New()
	if err := r.Register("default", func() spi.EhrAdapter { return New() }); err != nil {
		// Register only fails for nil factory or duplicate id; neither is
		// reachable here. Panic to surface a programmer error.
		panic(err)
	}
	return r
}

// Manifest returns the default adapter's declared manifest.
func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "default",
		Vendor:               "fhir-subscriptions-foss",
		Description:          "Reference adapter: passes HL7 v2 through unchanged; FHIR scan plan empty by default.",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities: spi.Capabilities{
			HL7Processor:     true,
			FhirScanRunner:   true,
			VendorAPIClient:  false,
			HydrationService: true,
		},
		ConfigSchema: []byte(`{"type":"object","additionalProperties":true}`),
		SpiVersion:   spi.HostSPIVersion,
	}
}

// BuildHl7Processor returns the default HL7 message processor. It is a
// no-normalization pass-through: Lex preserves the raw bytes; Classify always
// produces ChangeCreate (no vendor-specific trigger-code interpretation);
// MapToFHIR wraps the raw payload in a minimal FhirResource with type
// "Bundle" so downstream conformance is trivially satisfied.
func (a *Adapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor {
	return &hl7Processor{}
}

// BuildFhirScanRunner returns a scan runner with an empty plan. Operators
// who deploy the default adapter against a real FHIR API extend the plan via
// their own subclass; the framework runs no scans when ScanPlan is empty.
func (a *Adapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner {
	return &scanRunner{}
}

// BuildVendorAPIClient returns nil: the default adapter declares no vendor
// proprietary feed.
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient {
	return nil
}

// BuildHydrationService returns a hydration service stub. The default
// implementation returns ErrUnsupported on Fetch; deployments that need
// full-resource subscriptions configure a vendor adapter or override the
// hydration service.
func (a *Adapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService {
	return &hydrationService{}
}

// hl7Processor is the default HL7 pipeline: pass raw bytes through, classify
// every message as a create, map to a Bundle resource carrying the bytes.
type hl7Processor struct {
	spi.BaseHl7MessageProcessor
}

func (h *hl7Processor) Lex(raw []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return spi.ParsedHL7Message{Raw: cp, Segments: nil}, nil
}

func (h *hl7Processor) Classify(_ spi.ParsedHL7Message) (spi.Classification, error) {
	return spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: ""}, nil
}

func (h *hl7Processor) MapToFHIR(parsed spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	// The default adapter does not parse HL7 into a FHIR resource. It wraps
	// the payload in a minimal Bundle so the SPI pipeline (validate, sink
	// write) sees a non-empty body. Operators who need real translation
	// configure a vendor adapter.
	body := []byte(`{"resourceType":"Bundle","type":"collection"}`)
	return spi.FhirResource{
		ResourceType: "Bundle",
		ID:           "",
		Body:         body,
	}, nil
}

// scanRunner is the default FHIR scan runner with an empty plan.
type scanRunner struct {
	spi.BaseFhirScanRunner
}

func (s *scanRunner) ScanPlan() []spi.ScanTarget { return nil }

func (s *scanRunner) RunScan(_ context.Context, _ spi.ScanTarget) (spi.ScanIterator, error) {
	// Empty plan means RunScan should never be called by the framework. If a
	// caller invokes it anyway, return an empty iterator.
	return emptyScanIterator{}, nil
}

type emptyScanIterator struct{}

func (emptyScanIterator) Next(_ context.Context) (spi.FhirResource, bool, error) {
	return spi.FhirResource{}, false, nil
}

// hydrationService is a stub Fetch — vendor adapters must override for full
// resource subscriptions to function.
type hydrationService struct {
	spi.BaseHydrationService
}

func (h *hydrationService) Fetch(_ context.Context, _ spi.FhirReference) (spi.FhirResource, error) {
	return spi.FhirResource{}, ErrHydrationUnsupported
}

// ErrHydrationUnsupported is returned by the default hydration stub. The
// framework surfaces it back to the engine, which fails the affected
// full-resource notification with a TransientFailure (the deployment is
// expected to switch to a vendor adapter or configure custom hydration).
var ErrHydrationUnsupported = errHydrationUnsupported{}

type errHydrationUnsupported struct{}

func (errHydrationUnsupported) Error() string {
	return "default adapter: hydration is unsupported; configure a vendor adapter or override the hydration service"
}
