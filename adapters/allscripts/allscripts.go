// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package allscriptsadapter is the P3.2 SPI scaffold for Allscripts (Veradigm).
// It declares an Allscripts-specific manifest and constructs a passthrough
// Hl7MessageProcessor whose vendor mapping is still a TODO.
//
// Real Allscripts mapping needs HL7 v2 trigger-event handling per the
// Allscripts/Veradigm "Unity API" / "Sunrise" interface specs (Z-segments
// vary between Sunrise, TouchWorks, and Professional EHR product lines) and
// FHIR R4 mapping per the Veradigm developer portal
// (https://developer.veradigm.com/).
package allscriptsadapter

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
	if err := r.Register("allscripts", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "allscripts",
		Vendor:               "Allscripts (Veradigm)",
		Description:          "Scaffold adapter for Allscripts / Veradigm (P3.2). HL7 v2 Sunrise/TouchWorks/Pro Z-segments and Veradigm FHIR R4 mapping are TODO.",
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

// BuildHl7Processor returns a passthrough. TODO(allscripts): Sunrise vs
// TouchWorks vs Professional emit divergent Z-segments; Lex must dispatch on
// the Unity API source application before parsing. MapToFHIR targets Veradigm
// FHIR R4 profiles per https://developer.veradigm.com/.
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
