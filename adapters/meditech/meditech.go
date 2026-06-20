// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package meditechadapter is the SPI implementation for MEDITECH. It declares
// a MEDITECH-specific manifest and a Hl7MessageProcessor that lexes HL7 v2
// (including the lowercase "msh" header that MEDITECH MAGIC builds emit) via
// the shared internal/adapter/hl7v2 parser, and maps to FHIR R4 (Patient,
// Encounter, Observation, DiagnosticReport, ServiceRequest) per the MEDITECH
// Greenfield FHIR API.
//
// MEDITECH-specific Z-segments and Magic vs. C/S vs. Expanse dialect
// detection are out of scope for this base implementation.
package meditechadapter

import (
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/hl7v2"
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
		Description:          "Adapter for MEDITECH. HL7 v2 ADT/ORU/ORM (including lowercase msh from MAGIC builds) mapped to FHIR R4 per Greenfield API.",
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
	parsed, err := hl7v2.Parse(cp)
	if err != nil {
		return spi.ParsedHL7Message{Raw: cp}, err
	}
	return spi.ParsedHL7Message{Raw: cp, Segments: parsed}, nil
}

func (h *hl7Processor) Classify(parsed spi.ParsedHL7Message) (spi.Classification, error) {
	msg := messageFrom(parsed)
	if msg == nil {
		return spi.Classification{Kind: spi.ChangeCreate}, nil
	}
	trigger := strings.ToUpper(msg.TriggerEvent())
	return spi.Classification{Kind: classifyTrigger(trigger), CorrelationKey: msg.PatientID()}, nil
}

func classifyTrigger(trigger string) spi.ChangeKind {
	switch trigger {
	case "A03", "A23", "A29":
		return spi.ChangeDelete
	case "A02", "A08", "A11", "A13":
		return spi.ChangeUpdate
	}
	return spi.ChangeCreate
}

func (h *hl7Processor) MapToFHIR(parsed spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	msg := messageFrom(parsed)
	if msg == nil {
		var err error
		msg, err = hl7v2.Parse(parsed.Raw)
		if err != nil {
			return spi.FhirResource{}, err
		}
	}
	return hl7v2.MapToFHIR(msg)
}

func messageFrom(parsed spi.ParsedHL7Message) *hl7v2.Message {
	m, _ := parsed.Segments.(*hl7v2.Message)
	return m
}
