// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package spi defines the EHR Adapter SPI: the contract a vendor adapter
// implements. The SPI is a small framework — four sub-component interfaces
// (Hl7MessageProcessor, FhirScanRunner, VendorApiClient, HydrationService)
// plus a top-level EhrAdapter that registers them — together with the shared
// types they exchange with the host (ResourceChange, AdapterManifest,
// AdapterContext). The companion adapter-spi-framework LLD owns the
// host-side scaffolding that drives this SPI; this package is the contract
// itself.
package spi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ChangeKind is the FHIR-shaped change type carried on a ResourceChange.
type ChangeKind string

// ChangeKind values.
const (
	ChangeCreate ChangeKind = "create"
	ChangeUpdate ChangeKind = "update"
	ChangeDelete ChangeKind = "delete"
)

// Validate returns nil if the change kind is one of the three documented values.
func (k ChangeKind) Validate() error {
	switch k {
	case ChangeCreate, ChangeUpdate, ChangeDelete:
		return nil
	default:
		return fmt.Errorf("spi: invalid change kind %q", string(k))
	}
}

// FhirResource is the post-translation FHIR resource the adapter produces.
// Body is the canonical JSON serialization; ResourceType and ID are extracted
// for cheap downstream routing without a re-parse.
type FhirResource struct {
	ResourceType string
	ID           string
	Body         []byte
}

// ResourceChange is the durable, vendor-neutral output of every Stage 1
// adapter sub-component. The host writes one row per ResourceChange to
// resource_changes inside the same transaction that marks the source row
// processed.
type ResourceChange struct {
	ResourceType     string
	ChangeKind       ChangeKind
	Resource         FhirResource
	PreviousResource *FhirResource
	OccurredAt       time.Time
	CorrelationID    uuid.UUID
	EventCode        string
}

// Validate enforces the SPI invariants on a ResourceChange before the
// framework writes it. The framework calls this before every sink write;
// failure routes the source row to dead_letters.
func (c ResourceChange) Validate() error {
	if c.CorrelationID == uuid.Nil {
		return errors.New("spi: ResourceChange.CorrelationID must be set")
	}
	if err := c.ChangeKind.Validate(); err != nil {
		return err
	}
	if c.ResourceType == "" {
		return errors.New("spi: ResourceChange.ResourceType must be non-empty")
	}
	if c.Resource.ResourceType != c.ResourceType {
		return fmt.Errorf("spi: ResourceChange.ResourceType=%q does not match Resource.ResourceType=%q",
			c.ResourceType, c.Resource.ResourceType)
	}
	return nil
}

// SemVer is a simple major.minor.patch version. Used for spi_version on
// AdapterManifest and as the host-side SPI version constant.
type SemVer struct {
	Major int
	Minor int
	Patch int
}

// String returns the canonical "M.m.p" form.
func (v SemVer) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compatible reports whether an adapter built against `adapter` is compatible
// with a host running `v`. Per the SPI contract: major must match exactly;
// adapter minor must be <= host minor.
func (v SemVer) Compatible(adapter SemVer) bool {
	if v.Major != adapter.Major {
		return false
	}
	return adapter.Minor <= v.Minor
}

// VersionSpec is the manifest's declaration of which EHR versions the adapter
// supports. The v1 grammar is intentionally minimal: ">=X.Y" lower bound,
// exact "X.Y", or "*" any.
type VersionSpec string

// Satisfies reports whether this spec is satisfied by an operator-supplied pin
// string (`adapter.version_pin` in config).
//
// Relation: spec ">=A.B" satisfies pin ">=X.Y" iff A.B <= X.Y — the adapter
// must cover at least every version the pin covers. An adapter declaring "*"
// satisfies any pin. A pin of "*" is treated as no operator demand and is
// always satisfied.
func (v VersionSpec) Satisfies(pin string) bool {
	specLower, specIsAny, specOK := parseVersion(string(v))
	if !specOK {
		return false
	}
	if specIsAny {
		return true
	}
	pinLower, pinIsAny, pinOK := parseVersion(pin)
	if !pinOK {
		return false
	}
	if pinIsAny {
		return true
	}
	return cmpVersion(specLower, pinLower) <= 0
}

// parseVersion returns (lowerBound, isAny, ok). Lower bound is a slice of ints
// from "X.Y" or "X.Y.Z"; isAny is true for "*".
func parseVersion(s string) (lower []int, isAny, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false, false
	}
	if s == "*" {
		return nil, true, true
	}
	body := s
	if strings.HasPrefix(s, ">=") {
		body = strings.TrimSpace(strings.TrimPrefix(s, ">="))
	}
	parts := strings.Split(body, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, false, false
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false, false
		}
		out[i] = n
	}
	return out, false, true
}

// cmpVersion compares two version slices component-wise, padding shorter
// slices with zeros.
func cmpVersion(a, b []int) int {
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(a) {
			ai = a[i]
		}
		if i < len(b) {
			bi = b[i]
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// Capabilities describes which sub-components the adapter provides. The
// framework cross-checks each true field against the corresponding builder's
// non-nil return; a declared capability that the builder cannot produce is a
// fatal startup error.
type Capabilities struct {
	HL7Processor     bool
	FhirScanRunner   bool
	VendorAPIClient  bool
	HydrationService bool
}

// AdapterManifest is the top-level declaration the adapter returns from
// EhrAdapter.Manifest(). The host validates it before constructing
// sub-components.
type AdapterManifest struct {
	ID                   string
	Vendor               string
	Description          string
	SupportedEhrVersions VersionSpec
	Capabilities         Capabilities
	ConfigSchema         []byte
	ContributedTopics    [][]byte
	SpiVersion           SemVer
}

// Validate runs the static manifest validation rules from the framework LLD
// section 8: id pattern, non-empty vendor, non-zero spi major. Stateful
// checks (config-schema validity, contributed-topic URL collisions, capability
// vs builder consistency) live in the host loader, not on the manifest.
func (m AdapterManifest) Validate() error {
	if err := validateAdapterID(m.ID); err != nil {
		return err
	}
	if m.Vendor == "" {
		return errors.New("spi: AdapterManifest.Vendor must be non-empty")
	}
	if m.SpiVersion.Major == 0 && m.SpiVersion.Minor == 0 && m.SpiVersion.Patch == 0 {
		return errors.New("spi: AdapterManifest.SpiVersion must be non-zero")
	}
	return nil
}

// validateAdapterID enforces lowercase [a-z0-9-]+, no leading/trailing dash.
func validateAdapterID(id string) error {
	if id == "" {
		return errors.New("spi: AdapterManifest.ID must be non-empty")
	}
	if id[0] == '-' || id[len(id)-1] == '-' {
		return fmt.Errorf("spi: AdapterManifest.ID %q must not start or end with '-'", id)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return fmt.Errorf("spi: AdapterManifest.ID %q must match [a-z0-9-]+", id)
		}
	}
	return nil
}
