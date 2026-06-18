// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package directadapter is the P3.2 SPI scaffold for Direct messaging (the
// SMTP-based exchange standard from the Direct Project / DirectTrust). It
// targets facilities that only expose Direct, not HL7 v2 over MLLP and not a
// FHIR REST API.
//
// Real Direct mapping needs:
//   - SMTP/SMIME envelope handling per RFC 5322 + S/MIME RFC 5751
//   - XDM/XDR payload decoding per IHE ITI TF-2b §3.32
//     (https://profiles.ihe.net/ITI/TF/Volume2b/) -- Direct typically wraps
//     a CDA Continuity-of-Care Document in an XDM zip
//   - CDA → FHIR R4 transformation (Bundle of Patient/Encounter/etc.) per
//     the FHIR US-Core CDA → FHIR maps
//
// This stub takes the same shape as HL7 v2 adapters (Lex consumes raw bytes
// of the SMTP message body) so the framework's persistence + outbox path is
// exercised. The Lex / Classify / MapToFHIR methods are TODOs.
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

func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "direct",
		Vendor:               "Direct Project / DirectTrust",
		Description:          "Scaffold adapter for Direct (SMTP/S-MIME XDM messaging) per Direct Project + IHE XDM. CDA→FHIR mapping is TODO.",
		SupportedEhrVersions: spi.VersionSpec("*"),
		Capabilities: spi.Capabilities{
			HL7Processor:     true,
			FhirScanRunner:   false,
			VendorAPIClient:  false,
			HydrationService: false,
		},
		ConfigSchema: []byte(`{"type":"object","additionalProperties":true}`),
		SpiVersion:   spi.HostSPIVersion,
	}
}

// BuildHl7Processor returns a passthrough. TODO(direct): Direct messages are
// not HL7 v2 — the framework's Hl7MessageProcessor surface is reused as the
// "raw bytes in, FHIR resource out" pipeline. Lex must parse the SMTP+SMIME
// envelope, decode the XDM zip per IHE ITI TF-2b §3.32, and surface the inner
// CDA document; MapToFHIR must run a CDA→FHIR R4 transform.
func (a *Adapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor {
	return &hl7Processor{}
}

func (a *Adapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner   { return nil }
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient { return nil }
func (a *Adapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService {
	return nil
}

type hl7Processor struct {
	spi.BaseHl7MessageProcessor
}

func (h *hl7Processor) Lex(raw []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return spi.ParsedHL7Message{Raw: cp}, nil
}

func (h *hl7Processor) Classify(_ spi.ParsedHL7Message) (spi.Classification, error) {
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}

func (h *hl7Processor) MapToFHIR(_ spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	return spi.FhirResource{
		ResourceType: "Bundle",
		Body:         []byte(`{"resourceType":"Bundle","type":"collection"}`),
	}, nil
}
