// Copyright the fhir-ehr-subscriptions-service authors.
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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
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
		Vendor:               "fhir-ehr-subscriptions-service",
		Description:          "Reference adapter: passes HL7 v2 through unchanged; FHIR scan plan empty by default; vendor change-feed quiescent by default.",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities: spi.Capabilities{
			HL7Processor:     true,
			FhirScanRunner:   true,
			VendorAPIClient:  true,
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

// BuildVendorAPIClient returns a quiescent vendor change-feed client.
// The default adapter has no vendor proprietary feed configured, so
// Consume blocks until the host cancels the context — exactly mirroring
// the empty-plan FhirScanRunner pattern. Operators who deploy the
// default adapter against a real vendor API plug in a non-default
// adapter or override this method on a subclass.
//
// The framework's vendorclient.Worker treats a Consume that returns
// nil after ctx cancellation as the normal shutdown path; the worker
// never advances any cursor, never invokes Translate, and never writes
// to the resource_changes outbox while the change-feed is quiescent.
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient {
	return &vendorClient{}
}

// BuildHydrationService returns a hydration service. When the operator
// configures hydration.fhir_base_url (plumbed through AdapterContext as
// HydrationFhirBaseURL), the returned service dials that FHIR R5 endpoint
// for real GET /Type/id requests — exactly the production wire-up for a
// deployment that has a FHIR REST endpoint to fetch from. When the URL
// is empty, the service falls back to the legacy stub Fetch returning
// ErrHydrationUnsupported so the registry validation still sees a
// non-nil builder result and the host doesn't NPE on a delivery
// attempt.
func (a *Adapter) BuildHydrationService(actx spi.AdapterContext) spi.HydrationService {
	if actx.HydrationFhirBaseURL == "" {
		return &hydrationService{}
	}
	return &realHTTPHydration{
		base:   strings.TrimRight(actx.HydrationFhirBaseURL, "/"),
		client: &http.Client{Timeout: 10 * time.Second},
	}
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

// vendorClient is the default vendor change-feed client. It declares
// no proprietary feed: Consume blocks until the host cancels the
// context, then returns nil so the framework's vendorclient.Worker
// treats the exit as a normal shutdown. Translate is unreachable
// while the feed is quiescent and panics if called — operators who
// configure a real vendor feed plug in a non-default adapter, and a
// missed override surfaces as a loud panic rather than a silent
// no-op resource_changes write.
type vendorClient struct{}

// Consume blocks until ctx is cancelled then returns nil. With no
// vendor records ever pushed onto the sink, the worker's Translate
// path is never exercised on the default adapter.
func (vendorClient) Consume(ctx context.Context, _ spi.EventSink, _ []byte) error {
	<-ctx.Done()
	return nil
}

// Translate is unreachable on the default adapter (Consume never
// pushes a record). Panic surfaces a programming error if a future
// caller invokes it directly.
func (vendorClient) Translate(_ spi.VendorRecord) (spi.ResourceChange, error) {
	panic("default adapter: VendorAPIClient.Translate is unreachable while the change-feed is quiescent")
}

// hydrationService is a stub Fetch — vendor adapters must override for full
// resource subscriptions to function.
type hydrationService struct {
	spi.BaseHydrationService
}

func (h *hydrationService) Fetch(_ context.Context, _ spi.FhirReference) (spi.FhirResource, error) {
	return spi.FhirResource{}, ErrHydrationUnsupported
}

// realHTTPHydration is the default adapter's production HydrationService
// when the operator configures hydration.fhir_base_url. It performs real
// FHIR REST GETs against /<base>/<ResourceType>/<id> using the standard
// FHIR JSON content type. Vendor adapters that need bespoke auth, query
// parameters, or pagination override this with their own implementation;
// the default's job is to prove the wire-up end-to-end on any plain
// FHIR R5 server.
type realHTTPHydration struct {
	spi.BaseHydrationService
	base   string
	client *http.Client
}

func (r *realHTTPHydration) Fetch(ctx context.Context, ref spi.FhirReference) (spi.FhirResource, error) {
	url := fmt.Sprintf("%s/%s/%s", r.base, ref.ResourceType, ref.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("default hydration: build request: %w", err)
	}
	req.Header.Set("Accept", "application/fhir+json")
	resp, err := r.client.Do(req)
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("default hydration: fetch %s/%s: %w", ref.ResourceType, ref.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return spi.FhirResource{}, fmt.Errorf("default hydration: fetch %s/%s: status %d", ref.ResourceType, ref.ID, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return spi.FhirResource{}, fmt.Errorf("default hydration: read %s/%s: %w", ref.ResourceType, ref.ID, err)
	}
	return spi.FhirResource{
		ResourceType: ref.ResourceType,
		ID:           ref.ID,
		Body:         body,
	}, nil
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
