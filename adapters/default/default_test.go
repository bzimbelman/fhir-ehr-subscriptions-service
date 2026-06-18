// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package defaultadapter_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	defaultadapter "github.com/fhir-subscriptions-foss/fhir-subs/adapters/default"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/registry"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// The default adapter is the no-vendor reference: it satisfies the SPI shape,
// proves the framework can construct and call into a vendor adapter, and
// passes HL7 v2 input through without normalization. Vendor adapters
// (epic, meditech, ...) override what differs.

func TestDefaultManifestShape(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	m := a.Manifest()

	if m.ID != "default" {
		t.Errorf("Manifest.ID = %q, want default", m.ID)
	}
	if m.Vendor == "" {
		t.Error("Manifest.Vendor must be non-empty")
	}
	if m.SpiVersion != spi.HostSPIVersion {
		t.Errorf("Manifest.SpiVersion = %v, want %v", m.SpiVersion, spi.HostSPIVersion)
	}
	if !m.Capabilities.HL7Processor {
		t.Error("default adapter should declare HL7Processor capability")
	}
	if !m.Capabilities.FhirScanRunner {
		t.Error("default adapter should declare FhirScanRunner capability")
	}
	if !m.Capabilities.HydrationService {
		t.Error("default adapter should declare HydrationService capability")
	}
	if m.Capabilities.VendorAPIClient {
		t.Error("default adapter has no vendor proprietary feed and should not declare VendorAPIClient")
	}
	if err := m.Validate(); err != nil {
		t.Errorf("Manifest.Validate() = %v, want nil", err)
	}
}

func TestDefaultManifestIsRegistrable(t *testing.T) {
	t.Parallel()
	// Round-trip through the registry to prove the manifest is acceptable.
	r := defaultadapter.NewRegistered()

	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.HostSPIVersion,
	})
	if err != nil {
		t.Fatalf("registry.Load(default) = %v, want nil", err)
	}
	if a == nil || a.Manifest().ID != "default" {
		t.Errorf("loaded adapter id = %q, want default", a.Manifest().ID)
	}
}

func TestDefaultBuildersHonorCapabilities(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	ctx := spi.AdapterContext{Now: time.Now}

	if a.BuildHl7Processor(ctx) == nil {
		t.Error("BuildHl7Processor returned nil despite HL7Processor=true")
	}
	if a.BuildFhirScanRunner(ctx) == nil {
		t.Error("BuildFhirScanRunner returned nil despite FhirScanRunner=true")
	}
	if a.BuildHydrationService(ctx) == nil {
		t.Error("BuildHydrationService returned nil despite HydrationService=true")
	}
	if a.BuildVendorAPIClient(ctx) != nil {
		t.Error("BuildVendorAPIClient should be nil because VendorAPIClient=false")
	}
}

func TestDefaultHl7ProcessorPassesThrough(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	p := a.BuildHl7Processor(spi.AdapterContext{Now: time.Now})
	if p == nil {
		t.Fatal("BuildHl7Processor returned nil")
	}

	raw := []byte("MSH|^~\\&|EPIC|MAYO|MEDLINE|MAYO|202604261015||ORM^O01|1234|P|2.5\r")

	parsed, err := p.Lex(raw)
	if err != nil {
		t.Fatalf("Lex = %v, want nil", err)
	}
	if string(parsed.Raw) != string(raw) {
		t.Errorf("Lex did not preserve raw bytes (no normalization expected for default adapter)")
	}

	cls, err := p.Classify(parsed)
	if err != nil {
		t.Fatalf("Classify = %v, want nil", err)
	}
	if vErr := cls.Kind.Validate(); vErr != nil {
		t.Errorf("Classify.Kind invalid: %v", vErr)
	}

	resource, err := p.MapToFHIR(parsed, cls)
	if err != nil {
		t.Fatalf("MapToFHIR = %v, want nil", err)
	}
	if resource.ResourceType == "" {
		t.Error("MapToFHIR resource.ResourceType empty")
	}
	if len(resource.Body) == 0 {
		t.Error("MapToFHIR resource.Body empty")
	}

	// Validate is the OPTIONAL hook; the default adapter inherits the
	// permissive base impl per ADR 0010.
	if err := p.Validate(resource); err != nil {
		t.Errorf("Validate (base default) = %v, want nil", err)
	}

	// The unpaired-pair handlers from the base produce delete and create.
	now := time.Now().UTC()
	corr := uuid.New()
	d := p.OnUnpairedCancellation(resource, corr, now)
	if d.ChangeKind != spi.ChangeDelete {
		t.Errorf("OnUnpairedCancellation kind = %v, want delete", d.ChangeKind)
	}
	c := p.OnUnpairedReplacement(resource, corr, now)
	if c.ChangeKind != spi.ChangeCreate {
		t.Errorf("OnUnpairedReplacement kind = %v, want create", c.ChangeKind)
	}

	// Default hold window inherited from base.
	if got, want := p.CorrelationHoldWindow(), 30*time.Second; got != want {
		t.Errorf("CorrelationHoldWindow = %v, want %v", got, want)
	}
}

func TestDefaultFhirScanRunnerEmptyPlan(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	r := a.BuildFhirScanRunner(spi.AdapterContext{Now: time.Now})
	if r == nil {
		t.Fatal("BuildFhirScanRunner returned nil")
	}
	plan := r.ScanPlan()
	if len(plan) != 0 {
		t.Errorf("default ScanPlan = %v, want empty (no scans configured by default)", plan)
	}

	// Normalize is identity (inherited from base).
	src := spi.FhirResource{ResourceType: "Patient", ID: "p1", Body: []byte(`{}`)}
	if got := r.Normalize(src); got.ID != src.ID || got.ResourceType != src.ResourceType {
		t.Errorf("Normalize default not identity: got %+v want %+v", got, src)
	}
}

func TestDefaultHydrationCacheTTL(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	h := a.BuildHydrationService(spi.AdapterContext{Now: time.Now})
	if h == nil {
		t.Fatal("BuildHydrationService returned nil")
	}
	if got, want := h.CacheTTL(), 60*time.Second; got != want {
		t.Errorf("CacheTTL = %v, want %v", got, want)
	}
}

func TestDefaultLifecycleHooks(t *testing.T) {
	t.Parallel()
	a := defaultadapter.New()
	if err := a.OnStart(context.Background(), spi.AdapterContext{}); err != nil {
		t.Errorf("OnStart = %v, want nil", err)
	}
	if err := a.OnShutdown(context.Background(), spi.AdapterContext{}); err != nil {
		t.Errorf("OnShutdown = %v, want nil", err)
	}
}
