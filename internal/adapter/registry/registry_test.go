// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package registry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
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
		// Default to no declared capabilities so the registry's
		// capability vs builder cross-check (P1.10 follow-up #65)
		// has nothing to enforce. Tests that exercise the cross-check
		// set Capabilities explicitly.
		Capabilities: spi.Capabilities{},
		ConfigSchema: []byte(`{"type":"object"}`),
		SpiVersion:   spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	}
}

// builderAdapter is a stubAdapter whose Build* methods return
// configurable values (concrete or nil) so capability vs builder
// tests can construct precise mismatches.
type builderAdapter struct {
	spi.BaseEhrAdapter
	manifest   spi.AdapterManifest
	hl7        spi.Hl7MessageProcessor
	scanRunner spi.FhirScanRunner
	vendorAPI  spi.VendorAPIClient
	hydration  spi.HydrationService
}

func (b *builderAdapter) Manifest() spi.AdapterManifest { return b.manifest }
func (b *builderAdapter) BuildHl7Processor(spi.AdapterContext) spi.Hl7MessageProcessor {
	return b.hl7
}
func (b *builderAdapter) BuildFhirScanRunner(spi.AdapterContext) spi.FhirScanRunner {
	return b.scanRunner
}
func (b *builderAdapter) BuildVendorAPIClient(spi.AdapterContext) spi.VendorAPIClient {
	return b.vendorAPI
}
func (b *builderAdapter) BuildHydrationService(spi.AdapterContext) spi.HydrationService {
	return b.hydration
}

// stubHl7 / stubScan / stubVendor / stubHydration are minimal
// non-nil concrete implementations so a builder's "non-nil return"
// can be asserted without bringing in a vendor package.
type stubHl7 struct{ spi.BaseHl7MessageProcessor }
type stubScan struct{ spi.BaseFhirScanRunner }
type stubVendor struct{}

func (stubVendor) Consume(context.Context, spi.EventSink, []byte) error { return nil }
func (stubVendor) Translate(spi.VendorRecord) (spi.ResourceChange, error) {
	return spi.ResourceChange{}, nil
}

type stubHydration struct{ spi.BaseHydrationService }

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

// P1.10: a manifest whose ConfigSchema does not compile as a JSON
// Schema is rejected at Load.
func TestLoadRejectsInvalidConfigSchema(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	m.ConfigSchema = []byte(`{"type": 12345}`) // type must be a string
	_ = r.Register("default", newStubFactory(m))

	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with invalid config_schema = nil, want error")
	}
	var cse *registry.ManifestConfigSchemaError
	if !errors.As(err, &cse) {
		t.Fatalf("err = %v, want *ManifestConfigSchemaError", err)
	}
}

// P1.10: an adapter that contributes two topics with the same canonical
// URL is rejected at Load.
func TestLoadRejectsContributedTopicURLCollision(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	t1 := []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/x","version":"1"}`)
	t2 := []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/x","version":"2"}`)
	m.ContributedTopics = [][]byte{t1, t2}
	_ = r.Register("default", newStubFactory(m))

	_, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	})
	if err == nil {
		t.Fatal("Load with colliding contributed topics = nil, want error")
	}
	var cce *registry.ManifestContributedTopicCollisionError
	if !errors.As(err, &cce) {
		t.Fatalf("err = %v, want *ManifestContributedTopicCollisionError", err)
	}
}

// ---------- #65: Capability vs builder cross-check ----------

// capabilityCase covers one (capability, builder) pair. The test
// matrix sets exactly one capability=true and the matching builder
// to nil; Load must reject every row.
type capabilityCase struct {
	name       string
	build      func(m spi.AdapterManifest) *builderAdapter
	capability string
}

func TestLoadRejectsCapabilityWithoutBuilder(t *testing.T) {
	t.Parallel()
	cases := []capabilityCase{
		{
			name:       "HL7Processor declared but builder returns nil",
			capability: "HL7Processor",
			build: func(m spi.AdapterManifest) *builderAdapter {
				m.Capabilities = spi.Capabilities{HL7Processor: true}
				return &builderAdapter{
					manifest:   m,
					hl7:        nil,
					scanRunner: stubScan{},
					vendorAPI:  stubVendor{},
					hydration:  stubHydration{},
				}
			},
		},
		{
			name:       "FhirScanRunner declared but builder returns nil",
			capability: "FhirScanRunner",
			build: func(m spi.AdapterManifest) *builderAdapter {
				m.Capabilities = spi.Capabilities{FhirScanRunner: true}
				return &builderAdapter{
					manifest:   m,
					hl7:        stubHl7{},
					scanRunner: nil,
					vendorAPI:  stubVendor{},
					hydration:  stubHydration{},
				}
			},
		},
		{
			name:       "VendorAPIClient declared but builder returns nil",
			capability: "VendorAPIClient",
			build: func(m spi.AdapterManifest) *builderAdapter {
				m.Capabilities = spi.Capabilities{VendorAPIClient: true}
				return &builderAdapter{
					manifest:   m,
					hl7:        stubHl7{},
					scanRunner: stubScan{},
					vendorAPI:  nil,
					hydration:  stubHydration{},
				}
			},
		},
		{
			name:       "HydrationService declared but builder returns nil",
			capability: "HydrationService",
			build: func(m spi.AdapterManifest) *builderAdapter {
				m.Capabilities = spi.Capabilities{HydrationService: true}
				return &builderAdapter{
					manifest:   m,
					hl7:        stubHl7{},
					scanRunner: stubScan{},
					vendorAPI:  stubVendor{},
					hydration:  nil,
				}
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := registry.New()
			adapter := tc.build(goodManifest("default"))
			if err := r.Register("default", func() spi.EhrAdapter { return adapter }); err != nil {
				t.Fatalf("Register: %v", err)
			}
			_, err := r.Load(context.Background(), registry.LoadConfig{
				AdapterID:  "default",
				HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
			})
			if err == nil {
				t.Fatalf("Load with capability %s declared but nil builder = nil, want error",
					tc.capability)
			}
			var capErr *registry.ManifestCapabilityMismatchError
			if !errors.As(err, &capErr) {
				t.Fatalf("err = %T %v, want *ManifestCapabilityMismatchError", err, err)
			}
			if capErr.Capability != tc.capability {
				t.Errorf("capErr.Capability = %q, want %q", capErr.Capability, tc.capability)
			}
			if capErr.AdapterID != "default" {
				t.Errorf("capErr.AdapterID = %q, want default", capErr.AdapterID)
			}
		})
	}
}

func TestLoadAcceptsCapabilitiesWithMatchingBuilders(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	m.Capabilities = spi.Capabilities{
		HL7Processor:     true,
		FhirScanRunner:   true,
		VendorAPIClient:  true,
		HydrationService: true,
	}
	adapter := &builderAdapter{
		manifest:   m,
		hl7:        stubHl7{},
		scanRunner: stubScan{},
		vendorAPI:  stubVendor{},
		hydration:  stubHydration{},
	}
	if err := r.Register("default", func() spi.EhrAdapter { return adapter }); err != nil {
		t.Fatalf("Register: %v", err)
	}
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
}

// A capability declared as false with a nil builder must Load
// successfully — the absence is the entire point.
func TestLoadAllowsAbsentCapabilityWithNilBuilder(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	m.Capabilities = spi.Capabilities{HL7Processor: true} // others false
	adapter := &builderAdapter{
		manifest:   m,
		hl7:        stubHl7{},
		scanRunner: nil, // not declared, allowed
		vendorAPI:  nil, // not declared, allowed
		hydration:  nil, // not declared, allowed
	}
	if err := r.Register("default", func() spi.EhrAdapter { return adapter }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := r.Load(context.Background(), registry.LoadConfig{
		AdapterID:  "default",
		HostSpiVer: spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	}); err != nil {
		t.Fatalf("Load = %v, want nil", err)
	}
}

// ---------- #65: Cross-adapter contributed-topic URL collision ----------

func TestValidateAllRejectsCrossAdapterTopicURLCollision(t *testing.T) {
	t.Parallel()
	r := registry.New()

	t1 := []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/shared","version":"1"}`)
	t2 := []byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/shared","version":"1"}`)

	mA := goodManifest("vendor-a")
	mA.ContributedTopics = [][]byte{t1}
	mB := goodManifest("vendor-b")
	mB.ContributedTopics = [][]byte{t2}

	if err := r.Register("vendor-a", newStubFactory(mA)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("vendor-b", newStubFactory(mB)); err != nil {
		t.Fatal(err)
	}

	err := r.ValidateAll(context.Background(), spi.SemVer{Major: 1, Minor: 0, Patch: 0})
	if err == nil {
		t.Fatal("ValidateAll with cross-adapter URL collision = nil, want error")
	}
	var collisionErr *registry.CrossAdapterTopicCollisionError
	if !errors.As(err, &collisionErr) {
		t.Fatalf("err = %T %v, want *CrossAdapterTopicCollisionError", err, err)
	}
	if collisionErr.URL != "http://example.org/shared" {
		t.Errorf("URL = %q, want http://example.org/shared", collisionErr.URL)
	}
	// Both adapter ids must surface so operators see the conflict.
	got := collisionErr.AdapterIDs
	if len(got) != 2 {
		t.Fatalf("AdapterIDs = %v, want two entries", got)
	}
	wantSet := map[string]bool{"vendor-a": false, "vendor-b": false}
	for _, id := range got {
		wantSet[id] = true
	}
	for id, seen := range wantSet {
		if !seen {
			t.Errorf("AdapterIDs missing %q: got %v", id, got)
		}
	}
}

func TestValidateAllAcceptsDistinctTopicURLs(t *testing.T) {
	t.Parallel()
	r := registry.New()

	mA := goodManifest("vendor-a")
	mA.ContributedTopics = [][]byte{
		[]byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/a","version":"1"}`),
	}
	mB := goodManifest("vendor-b")
	mB.ContributedTopics = [][]byte{
		[]byte(`{"resourceType":"SubscriptionTopic","url":"http://example.org/b","version":"1"}`),
	}

	if err := r.Register("vendor-a", newStubFactory(mA)); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("vendor-b", newStubFactory(mB)); err != nil {
		t.Fatal(err)
	}

	if err := r.ValidateAll(context.Background(), spi.SemVer{Major: 1, Minor: 0, Patch: 0}); err != nil {
		t.Fatalf("ValidateAll = %v, want nil", err)
	}
}

// ValidateAll must surface the existing per-adapter manifest
// errors (e.g. invalid config schema) for adapters in the registry,
// because it is the startup-time gate the host calls.
func TestValidateAllSurfacesPerAdapterErrors(t *testing.T) {
	t.Parallel()
	r := registry.New()
	m := goodManifest("default")
	m.ConfigSchema = []byte(`{"type": 12345}`) // invalid
	if err := r.Register("default", newStubFactory(m)); err != nil {
		t.Fatal(err)
	}
	err := r.ValidateAll(context.Background(), spi.SemVer{Major: 1, Minor: 0, Patch: 0})
	if err == nil {
		t.Fatal("ValidateAll with invalid config_schema = nil, want error")
	}
	var cse *registry.ManifestConfigSchemaError
	if !errors.As(err, &cse) {
		t.Fatalf("err = %T %v, want *ManifestConfigSchemaError", err, err)
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
