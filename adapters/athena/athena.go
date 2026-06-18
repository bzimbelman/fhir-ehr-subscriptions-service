// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package athenaadapter is the P3.2 SPI scaffold for athenahealth. It declares
// an athenahealth-specific manifest and constructs a passthrough
// Hl7MessageProcessor whose vendor mapping is still a TODO.
//
// Real athena mapping is mostly REST-API-driven (athenaNet APIs / athena
// FHIR R4 endpoints, https://docs.athenahealth.com/api/) rather than HL7 v2;
// this stub keeps the HL7 surface for facilities that still emit v2 over MLLP
// to athena bridges, but the production adapter will likely be VendorAPIClient
// dominant.
package athenaadapter

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
	if err := r.Register("athena", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "athena",
		Vendor:               "athenahealth",
		Description:          "Scaffold adapter for athenahealth (P3.2). athenaNet REST + FHIR R4 mapping is TODO; production adapter will likely be VendorAPIClient-dominant.",
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

// BuildHl7Processor returns a passthrough. TODO(athena): real production
// integration likely lives in BuildVendorAPIClient against athenaNet REST + FHIR
// R4 endpoints (https://docs.athenahealth.com/api/); HL7 v2 path here exists
// for bridge configurations.
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
