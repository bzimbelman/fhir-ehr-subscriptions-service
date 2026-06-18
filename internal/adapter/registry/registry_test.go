// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/registry"
	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// stubAdapter satisfies spi.EhrAdapter with a configurable manifest.
type stubAdapter struct {
	spi.BaseEhrAdapter
	manifest spi.AdapterManifest
}

func (s *stubAdapter) Manifest() spi.AdapterManifest { return s.manifest }
func (s *stubAdapter) BuildHl7Processor(spi.AdapterContext) spi.Hl7MessageProcessor {
	return nil
}
func (s *stubAdapter) BuildFhirScanRunner(spi.AdapterContext) spi.FhirScanRunner { return nil }
func (s *stubAdapter) BuildVendorAPIClient(spi.AdapterContext) spi.VendorAPIClient {
	return nil
}
func (s *stubAdapter) BuildHydrationService(spi.AdapterContext) spi.HydrationService {
	return nil
}

func newStubFactory(m spi.AdapterManifest) registry.Factory {
	return func() spi.EhrAdapter { return &stubAdapter{manifest: m} }
}

func goodManifest(id string) spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   id,
		Vendor:               "test",
		Description:          "stub",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities:         spi.Capabilities{HL7Processor: true},
		ConfigSchema:         []byte(`{"type":"object"}`),
		SpiVersion:           spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	}
}

func TestRegisterAndIDs(t *testing.T) {
	t.Parallel()
	r := registry.New()
	if err := r.Register("default", newStubFactory(goodManifest("default"))); err != nil {
		t.Fatalf("Register(default) = %v, want nil", err)
	}
	if err := r.Register("epic", newStubFactory(goodManifest("epic"))); err != nil {
		t.Fatalf("Register(epic) = %v, want nil", err)
	}
	ids := r.IDs()
	if len(ids) != 2 {
		t.Fatalf("IDs() = %v, want 2 entries", ids)
	}
	// IDs returned sorted for stable startup-error messages.
	if ids[0] != "default" || ids[1] != "epic" {
		t.Errorf("IDs() = %v, want [default epic] (sorted)", ids)
	}
}

func TestRegisterDuplicateRejected(t *testing.T) {
	t.Parallel()
	r := registry.New()
	if err := r.Register("default", newStubFactory(goodManifest("default"))); err != nil {
		t.Fatal(err)
	}
	err := r.Register("default", newStubFactory(goodManifest("default")))
	if err == nil {
		t.Fatal("Register duplicate id = nil, want error")
	}
}

func TestRegisterEmptyIDRejected(t *testing.T) {
	t.Parallel()
	r := registry.New()
	if err := r.Register("", newStubFactory(goodManifest(""))); err == nil {
		t.Fatal("Register empty id = nil, want error")
	}
}

func TestRegisterNilFactoryRejected(t *testing.T) {
	t.Parallel()
	r := registry.New()
	if err := r.Register("default", nil); err == nil {
		t.Fatal("Register nil factory = nil, want error")
	}
}

// ---------- Load: config-driven selection per LLD section 4 ----------

func TestLoadUnknownAdapter(t *testing.T) {
	t.Parallel()
	r := registry.New()
	_ = r.Register("default", newStubFactory(goodManifest("default")))

	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "epic",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with unknown id = nil, want error")
	}
	var unknownErr *registry.UnknownAdapterError
	if !errors.As(err, &unknownErr) {
		t.Fatalf("error is not *UnknownAdapterError: %T: %v", err, err)
	}
	if unknownErr.Requested != "epic" {
		t.Errorf("Requested = %q, want epic", unknownErr.Requested)
	}
	// Bundled list must surface in the error so operators can see the options.
	if len(unknownErr.Bundled) == 0 || !strings.Contains(strings.Join(unknownErr.Bundled, ","), "default") {
		t.Errorf("Bundled = %v, want list including 'default'", unknownErr.Bundled)
	}
}

func TestLoadSpiMajorMismatch(t *testing.T) {
	t.Parallel()
	r := registry.New()
	_ = r.Register("default", newStubFactory(spi.AdapterManifest{
		ID:                   "default",
		Vendor:               "test",
		SupportedEhrVersions: spi.VersionSpec("*"),
		SpiVersion:           spi.SemVer{Major: 2, Minor: 0, Patch: 0},
	}))
	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with major mismatch = nil, want error")
	}
	var mismatchErr *registry.SpiMajorMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("error is not *SpiMajorMismatchError: %T: %v", err, err)
	}
}

func TestLoadManifestIDMismatch(t *testing.T) {
	t.Parallel()
	r := registry.New()
	// Register under id "default" but the factory hands back a manifest
	// claiming id "epic". This is the defensive check from LLD section 4.
	_ = r.Register("default", newStubFactory(goodManifest("epic")))

	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with manifest id mismatch = nil, want error")
	}
	var mismatchErr *registry.ManifestIDMismatchError
	if !errors.As(err, &mismatchErr) {
		t.Fatalf("error is not *ManifestIDMismatchError: %T: %v", err, err)
	}
}

func TestLoadVersionPinUnsatisfiable(t *testing.T) {
	t.Parallel()
	r := registry.New()
	// Adapter declares it only supports >=2025.1; operator pins >=2024.1.
	m := goodManifest("default")
	m.SupportedEhrVersions = spi.VersionSpec(">=2025.1")
	_ = r.Register("default", newStubFactory(m))

	pin := ">=2024.1"
	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
		VersionPin: &pin,
	})
	if err == nil {
		t.Fatal("Load with unsatisfiable pin = nil, want error")
	}
	var pinErr *registry.VersionPinUnsatisfiableError
	if !errors.As(err, &pinErr) {
		t.Fatalf("error is not *VersionPinUnsatisfiableError: %T: %v", err, err)
	}
}

func TestLoadVersionPinAbsentSkipsCheck(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	m.SupportedEhrVersions = spi.VersionSpec(">=2025.1")
	_ = r.Register("default", newStubFactory(m))

	// No version pin means no check.
	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
		VersionPin: nil,
	})
	if err != nil {
		t.Fatalf("Load(no pin) = %v, want nil", err)
	}
	if a == nil {
		t.Fatal("Load returned nil adapter")
	}
}

func TestLoadInvalidManifest(t *testing.T) {
	t.Parallel()
	r := registry.New()
	// Register a factory whose manifest is structurally invalid (empty vendor).
	m := goodManifest("default")
	m.Vendor = ""
	_ = r.Register("default", newStubFactory(m))

	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with invalid manifest = nil, want error")
	}
}

func TestLoadHappyPath(t *testing.T) {
	t.Parallel()
	r := registry.New()
	_ = r.Register("default", newStubFactory(goodManifest("default")))

	a, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
	if a == nil {
		t.Fatal("Load returned nil adapter")
	}
	if a.Manifest().ID != "default" {
		t.Errorf("Manifest().ID = %q, want default", a.Manifest().ID)
	}
}

// HostSPIVersion is the constant the registry compares adapter manifests
// against. Verify it is exposed and non-zero so the host can pass it.
func TestHostSPIVersionExposed(t *testing.T) {
	t.Parallel()
	v := spi.HostSPIVersion
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 {
		t.Error("spi.HostSPIVersion is zero; it must be a non-zero version")
	}
}
