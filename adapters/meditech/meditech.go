// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package meditechadapter is the P3.2 SPI scaffold for MEDITECH. It declares a
// MEDITECH-specific manifest and constructs a passthrough Hl7MessageProcessor
// whose vendor mapping is still a TODO.
//
// Real MEDITECH mapping needs HL7 v2 ADT/ORM/ORU handling per MEDITECH's
// "NPR Toolbox" + "Expanse Integration Engine" interface specs
// (Z-segments differ between Magic, C/S, and Expanse releases) and FHIR R4
// mapping per the MEDITECH Greenfield FHIR API
// (https://home.meditech.com/en/d/restapiresources/).
package meditechadapter

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
	if err := r.Register("meditech", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "meditech",
		Vendor:               "MEDITECH",
		Description:          "Scaffold adapter for MEDITECH (P3.2). HL7 v2 Magic/C-S/Expanse Z-segments and Greenfield FHIR R4 mapping are TODO.",
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

// BuildHl7Processor returns a passthrough. TODO(meditech): MEDITECH HL7 v2
// segments differ across Magic, C/S, and Expanse releases; Lex must branch on
// MSH-3/MSH-4 sender app/facility before parsing Z-segments. MapToFHIR targets
// MEDITECH Greenfield FHIR R4 profiles per
// https://home.meditech.com/en/d/restapiresources/.
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
