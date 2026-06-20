// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package directadapter is the SPI placeholder for Direct messaging (the
// SMTP-based exchange standard from the Direct Project / DirectTrust). It
// targets facilities that only expose Direct, not HL7 v2 over MLLP and not a
// FHIR REST API.
//
// OP #175: Direct messages are SMTP+S/MIME envelopes carrying XDM/CDA, NOT
// HL7 v2 over MLLP. The previous manifest declared HL7Processor=true and
// returned a passthrough Hl7MessageProcessor that lex'd raw SMTP bytes and
// emitted a hardcoded {"resourceType":"Bundle","type":"collection"} —
// a capability lie that would let the framework route SMTP traffic into
// the MLLP pipeline. The honest manifest declares NO capabilities until
// SMTP/S-MIME ingress is wired (separate story). The adapter remains
// loadable so operators see Direct in the registry, but every Build*
// method returns nil and no subsystem will mis-route Direct traffic.
//
// A future Direct implementation needs:
//   - SMTP/S-MIME envelope handling per RFC 5322 + S/MIME RFC 5751
//   - XDM/XDR payload decoding per IHE ITI TF-2b §3.32
//     (https://profiles.ihe.net/ITI/TF/Volume2b/) — Direct typically wraps
//     a CDA Continuity-of-Care Document in an XDM zip
//   - CDA -> FHIR R4 transformation (Bundle of Patient/Encounter/etc.) per
//     the FHIR US-Core CDA -> FHIR maps
//   - A webhook-style ingress that accepts SMTP body bytes and validates
//     S/MIME signatures
package directadapter

import (
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

type Adapter struct {
	spi.BaseEhrAdapter
}

func New() *Adapter { return &Adapter{} }

func NewRegistered() *registry.Registry {
	r := registry.New()
	if err := r.Register("direct", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

// Manifest declares the Direct adapter with NO capabilities. SMTP/S-MIME
// XDM ingress and CDA -> FHIR mapping are unimplemented; the registry's
// validateCapabilities check would reject any capability=true paired with
// a nil builder, and pretending otherwise is the lie OP #175 fixes.
func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "direct",
		Vendor:               "Direct Project / DirectTrust",
		Description:          "Placeholder adapter for Direct (SMTP/S-MIME XDM messaging). No capabilities declared until SMTP ingress + CDA->FHIR mapping ship.",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities:         spi.Capabilities{},
		ConfigSchema:         []byte(`{"type":"object","additionalProperties":true}`),
		SpiVersion:           spi.HostSPIVersion,
	}
}

// All Build* methods return nil. The manifest declares no capabilities, so
// the registry's validateCapabilities check accepts the nil returns and
// the host never invokes any subsystem against this adapter. SMTP/S-MIME
// support is a separate story.
func (a *Adapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor  { return nil }
func (a *Adapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner     { return nil }
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient   { return nil }
func (a *Adapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService { return nil }
