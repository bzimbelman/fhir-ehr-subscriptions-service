// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package epicadapter is the SPI implementation for Epic Systems. It declares
// an Epic-specific manifest and a Hl7MessageProcessor that lexes HL7 v2 with
// the shared internal/adapter/hl7v2 parser, classifies per Epic Bridges
// trigger conventions, and maps to FHIR R4 (Patient, Encounter, Observation,
// DiagnosticReport, ServiceRequest) per the Epic Interconnect FHIR R4 IG.
//
// Epic-specific Z-segments (Z*Notes, ZPV, ZBR, etc.) are out of scope for
// this base implementation and are tracked under follow-on stories per Epic
// deployment.
package epicadapter

import (
	"strings"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/hl7v2"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// Adapter is the Epic Systems EHR adapter.
type Adapter struct {
	spi.BaseEhrAdapter
}

// New constructs a fresh Epic adapter instance.
func New() *Adapter { return &Adapter{} }

// NewRegistered constructs a registry pre-populated with the Epic adapter.
func NewRegistered() *registry.Registry {
	r := registry.New()
	if err := r.Register("epic", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

// Manifest declares Epic-specific identifiers.
func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "epic",
		Vendor:               "Epic Systems",
		Description:          "Adapter for Epic Systems. HL7 v2 ADT/ORU/ORM mapped to FHIR R4 per Interconnect IG.",
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
