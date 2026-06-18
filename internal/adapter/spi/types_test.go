// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package spi_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

func TestChangeKindValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		kind  spi.ChangeKind
		valid bool
	}{
		{spi.ChangeCreate, true},
		{spi.ChangeUpdate, true},
		{spi.ChangeDelete, true},
		{spi.ChangeKind(""), false},
		{spi.ChangeKind("modify"), false},
	}
	for _, tc := range tests {
		err := tc.kind.Validate()
		if tc.valid && err != nil {
			t.Errorf("ChangeKind(%q).Validate() = %v, want nil", tc.kind, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("ChangeKind(%q).Validate() = nil, want error", tc.kind)
		}
	}
}

func TestFhirResourceFields(t *testing.T) {
	t.Parallel()
	r := spi.FhirResource{
		ResourceType: "ServiceRequest",
		ID:           "abc-123",
		Body:         []byte(`{"resourceType":"ServiceRequest","id":"abc-123"}`),
	}
	if r.ResourceType != "ServiceRequest" {
		t.Errorf("ResourceType = %q, want ServiceRequest", r.ResourceType)
	}
	if r.ID != "abc-123" {
		t.Errorf("ID = %q, want abc-123", r.ID)
	}
	if len(r.Body) == 0 {
		t.Error("Body should be populated")
	}
}

func TestResourceChangeShape(t *testing.T) {
	t.Parallel()
	corrID := uuid.New()
	now := time.Now().UTC()
	prev := &spi.FhirResource{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{}`)}
	rc := spi.ResourceChange{
		ResourceType:     "ServiceRequest",
		ChangeKind:       spi.ChangeUpdate,
		Resource:         spi.FhirResource{ResourceType: "ServiceRequest", ID: "def", Body: []byte(`{}`)},
		PreviousResource: prev,
		OccurredAt:       now,
		CorrelationID:    corrID,
		EventCode:        "",
	}
	if err := rc.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestResourceChangeValidateRejectsZeroCorrelationID(t *testing.T) {
	t.Parallel()
	rc := spi.ResourceChange{
		ResourceType: "ServiceRequest",
		ChangeKind:   spi.ChangeCreate,
		Resource:     spi.FhirResource{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{}`)},
		OccurredAt:   time.Now().UTC(),
		// CorrelationID intentionally zero
	}
	if err := rc.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for zero correlation_id")
	}
}

func TestResourceChangeValidateRejectsBadKind(t *testing.T) {
	t.Parallel()
	rc := spi.ResourceChange{
		ResourceType:  "ServiceRequest",
		ChangeKind:    spi.ChangeKind("bogus"),
		Resource:      spi.FhirResource{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{}`)},
		OccurredAt:    time.Now().UTC(),
		CorrelationID: uuid.New(),
	}
	if err := rc.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for invalid change kind")
	}
}

func TestResourceChangeValidateRequiresResourceTypeMatch(t *testing.T) {
	t.Parallel()
	// The outer ResourceType must match the embedded Resource.ResourceType.
	rc := spi.ResourceChange{
		ResourceType:  "ServiceRequest",
		ChangeKind:    spi.ChangeCreate,
		Resource:      spi.FhirResource{ResourceType: "Observation", ID: "abc", Body: []byte(`{}`)},
		OccurredAt:    time.Now().UTC(),
		CorrelationID: uuid.New(),
	}
	if err := rc.Validate(); err == nil {
		t.Fatal("Validate() = nil, want error for resource_type mismatch")
	}
}

func TestSemVerString(t *testing.T) {
	t.Parallel()
	v := spi.SemVer{Major: 1, Minor: 2, Patch: 3}
	if got, want := v.String(), "1.2.3"; got != want {
		t.Errorf("SemVer.String() = %q, want %q", got, want)
	}
}

func TestSemVerCompatible(t *testing.T) {
	t.Parallel()
	host := spi.SemVer{Major: 1, Minor: 5, Patch: 0}
	cases := []struct {
		adapter spi.SemVer
		ok      bool
	}{
		{spi.SemVer{Major: 1, Minor: 5, Patch: 0}, true},
		{spi.SemVer{Major: 1, Minor: 4, Patch: 9}, true},  // adapter older minor: ok
		{spi.SemVer{Major: 1, Minor: 6, Patch: 0}, false}, // adapter newer minor than host: not ok per LLD
		{spi.SemVer{Major: 2, Minor: 0, Patch: 0}, false}, // major mismatch
		{spi.SemVer{Major: 0, Minor: 9, Patch: 0}, false}, // major mismatch
	}
	for _, tc := range cases {
		got := host.Compatible(tc.adapter)
		if got != tc.ok {
			t.Errorf("host=%s.Compatible(adapter=%s) = %v, want %v", host, tc.adapter, got, tc.ok)
		}
	}
}

func TestVersionSpecSatisfies(t *testing.T) {
	t.Parallel()
	// Per LLD section 4 (adapter-spi-framework.md): VersionPinUnsatisfiable
	// when the operator's version_pin asks for support the adapter does not
	// declare. Receiver = manifest.supported_ehr_versions; argument = the
	// operator's adapter.version_pin from config.
	//
	// Relation: spec ">=A.B" satisfies pin ">=X.Y" iff A.B <= X.Y. The
	// adapter must cover at least every version the pin covers; an adapter
	// with a stricter (higher) lower bound is too restrictive. Per the LLD
	// example: operator pins ">=2024.1" but adapter declares ">=2025.1" =>
	// unsatisfied.
	spec := spi.VersionSpec(">=2024.1")
	cases := []struct {
		pin string
		ok  bool
	}{
		{">=2024.1", true},  // pin equals spec
		{">=2025.0", true},  // pin newer-only; spec covers it
		{">=2023.0", false}, // pin reaches further back than spec covers
		{"*", true},         // pin wildcard treated as no demand
	}
	for _, tc := range cases {
		got := spec.Satisfies(tc.pin)
		if got != tc.ok {
			t.Errorf("VersionSpec(%q).Satisfies(%q) = %v, want %v", spec, tc.pin, got, tc.ok)
		}
	}

	// "*" spec satisfies any pin.
	any := spi.VersionSpec("*")
	for _, pin := range []string{"*", ">=2024.1", "2024.1"} {
		if !any.Satisfies(pin) {
			t.Errorf("VersionSpec(\"*\").Satisfies(%q) = false, want true", pin)
		}
	}
}

func TestAdapterManifestValidate(t *testing.T) {
	t.Parallel()
	good := spi.AdapterManifest{
		ID:                   "default",
		Vendor:               "fhir-subscriptions-foss",
		Description:          "no-vendor reference adapter",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities: spi.Capabilities{
			HL7Processor:     true,
			FhirScanRunner:   true,
			VendorAPIClient:  false,
			HydrationService: true,
		},
		ConfigSchema:      []byte(`{"type":"object"}`),
		ContributedTopics: nil,
		SpiVersion:        spi.SemVer{Major: 1, Minor: 0, Patch: 0},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	cases := map[string]func(*spi.AdapterManifest){
		"empty id":         func(m *spi.AdapterManifest) { m.ID = "" },
		"upper-case id":    func(m *spi.AdapterManifest) { m.ID = "Default" },
		"id with space":    func(m *spi.AdapterManifest) { m.ID = "default adapter" },
		"id leading dash":  func(m *spi.AdapterManifest) { m.ID = "-default" },
		"id trailing dash": func(m *spi.AdapterManifest) { m.ID = "default-" },
		"empty vendor":     func(m *spi.AdapterManifest) { m.Vendor = "" },
		"zero spi major":   func(m *spi.AdapterManifest) { m.SpiVersion = spi.SemVer{Major: 0, Minor: 0, Patch: 0} },
	}
	for name, mutate := range cases {
		bad := good
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("Validate(%s) = nil, want error", name)
		}
	}
}
