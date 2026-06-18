// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package spi_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// ---------- Hl7MessageProcessor ----------

func TestBaseHl7MessageProcessorDefaults(t *testing.T) {
	t.Parallel()
	base := spi.BaseHl7MessageProcessor{}

	// Default hold window per architecture / contracts/adapter-spi.md = 30s.
	if got, want := base.CorrelationHoldWindow(), 30*time.Second; got != want {
		t.Errorf("CorrelationHoldWindow() = %v, want %v", got, want)
	}

	// Default unpaired cancellation: emits a plain delete change.
	r := spi.FhirResource{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{}`)}
	delChange := base.OnUnpairedCancellation(r, uuid.New(), time.Now().UTC())
	if delChange.ChangeKind != spi.ChangeDelete {
		t.Errorf("OnUnpairedCancellation kind = %v, want delete", delChange.ChangeKind)
	}
	if delChange.ResourceType != "ServiceRequest" {
		t.Errorf("OnUnpairedCancellation resource_type = %q, want ServiceRequest", delChange.ResourceType)
	}

	// Default unpaired replacement: emits a plain create.
	createChange := base.OnUnpairedReplacement(r, uuid.New(), time.Now().UTC())
	if createChange.ChangeKind != spi.ChangeCreate {
		t.Errorf("OnUnpairedReplacement kind = %v, want create", createChange.ChangeKind)
	}

	// Default Validate: nil for any resource (the swap-hook validator from
	// ADR 0010 wires in stricter validation in v2; v1 base is permissive).
	if err := base.Validate(r); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestHl7MessageProcessorInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	// Vendor adapters embed BaseHl7MessageProcessor and override REQUIRED
	// methods to satisfy the interface.
	v := &fakeHl7Processor{}
	var _ spi.Hl7MessageProcessor = v
}

// ---------- FhirScanRunner ----------

func TestBaseFhirScanRunnerDefaults(t *testing.T) {
	t.Parallel()
	base := spi.BaseFhirScanRunner{}

	r := spi.FhirResource{ResourceType: "Patient", ID: "p1", Body: []byte(`{"resourceType":"Patient","id":"p1"}`)}

	// Normalize default = identity.
	if got := base.Normalize(r); got.ResourceType != r.ResourceType || got.ID != r.ID || string(got.Body) != string(r.Body) {
		t.Errorf("Normalize default not identity: got %+v", got)
	}

	// ContentHash default: deterministic SHA-256 of canonical body.
	h1 := base.ContentHash(r)
	h2 := base.ContentHash(r)
	if h1 != h2 {
		t.Errorf("ContentHash not deterministic: %s vs %s", h1, h2)
	}
	r2 := r
	r2.Body = []byte(`{"resourceType":"Patient","id":"p2"}`)
	h3 := base.ContentHash(r2)
	if h1 == h3 {
		t.Errorf("ContentHash collided across distinct bodies")
	}
	// Hash should be a non-empty string.
	if h1 == "" {
		t.Error("ContentHash returned empty")
	}
}

func TestFhirScanRunnerInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	v := &fakeScanRunner{}
	var _ spi.FhirScanRunner = v
}

// ---------- VendorAPIClient ----------

func TestVendorAPIClientInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	v := &fakeVendorAPIClient{}
	var _ spi.VendorAPIClient = v
}

// ---------- HydrationService ----------

func TestBaseHydrationServiceDefaults(t *testing.T) {
	t.Parallel()
	base := spi.BaseHydrationService{}
	if got, want := base.CacheTTL(), 60*time.Second; got != want {
		t.Errorf("CacheTTL() = %v, want %v", got, want)
	}
}

func TestHydrationServiceInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	v := &fakeHydration{}
	var _ spi.HydrationService = v
}

// ---------- EhrAdapter ----------

func TestBaseEhrAdapterDefaults(t *testing.T) {
	t.Parallel()
	base := spi.BaseEhrAdapter{}
	// OnStart and OnShutdown default to no-op returning nil.
	if err := base.OnStart(context.Background(), spi.AdapterContext{}); err != nil {
		t.Errorf("OnStart() = %v, want nil", err)
	}
	if err := base.OnShutdown(context.Background(), spi.AdapterContext{}); err != nil {
		t.Errorf("OnShutdown() = %v, want nil", err)
	}
}

func TestEhrAdapterInterfaceSatisfied(t *testing.T) {
	t.Parallel()
	v := &fakeAdapter{}
	var _ spi.EhrAdapter = v
}

// ---------- Fakes used by interface-satisfaction asserts ----------

type fakeHl7Processor struct {
	spi.BaseHl7MessageProcessor
}

func (f *fakeHl7Processor) Lex(_ []byte) (spi.ParsedHL7Message, error) {
	return spi.ParsedHL7Message{}, nil
}
func (f *fakeHl7Processor) Classify(_ spi.ParsedHL7Message) (spi.Classification, error) {
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}
func (f *fakeHl7Processor) MapToFHIR(_ spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	return spi.FhirResource{ResourceType: "ServiceRequest", ID: "x", Body: []byte(`{}`)}, nil
}

type fakeScanRunner struct {
	spi.BaseFhirScanRunner
}

func (f *fakeScanRunner) ScanPlan() []spi.ScanTarget {
	return []spi.ScanTarget{{ResourceType: "Patient", Cadence: time.Minute}}
}

func (f *fakeScanRunner) RunScan(_ context.Context, _ spi.ScanTarget) (spi.ScanIterator, error) {
	return emptyIterator{}, nil
}

type emptyIterator struct{}

func (emptyIterator) Next(_ context.Context) (spi.FhirResource, bool, error) {
	return spi.FhirResource{}, false, nil
}

type fakeVendorAPIClient struct{}

func (f *fakeVendorAPIClient) Consume(_ context.Context, _ spi.EventSink, _ []byte) error {
	return nil
}
func (f *fakeVendorAPIClient) Translate(_ spi.VendorRecord) (spi.ResourceChange, error) {
	return spi.ResourceChange{}, nil
}

type fakeHydration struct {
	spi.BaseHydrationService
}

func (f *fakeHydration) Fetch(_ context.Context, _ spi.FhirReference) (spi.FhirResource, error) {
	return spi.FhirResource{}, nil
}

type fakeAdapter struct {
	spi.BaseEhrAdapter
}

func (f *fakeAdapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "fake",
		Vendor:               "test",
		SupportedEhrVersions: spi.VersionSpec("*"),
		SpiVersion:           spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	}
}
func (f *fakeAdapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor {
	return nil
}
func (f *fakeAdapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner {
	return nil
}
func (f *fakeAdapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient {
	return nil
}
func (f *fakeAdapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService {
	return nil
}
