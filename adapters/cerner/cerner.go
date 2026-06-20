// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package cerneradapter is the SPI implementation for Oracle Health (Cerner).
// It declares a Cerner-specific manifest and a Hl7MessageProcessor that
// lexes HL7 v2 with the shared internal/adapter/hl7v2 parser, classifies
// per Cerner Open Engine trigger conventions (ADT^A03 -> delete, ADT^A08 ->
// update, etc.), and maps to FHIR R4 (Patient, Encounter, Observation,
// DiagnosticReport, ServiceRequest) per the Cerner Millennium FHIR R4 IG
// (https://fhir.cerner.com/millennium/r4/).
//
// Cerner-specific Z-segments (e.g. ZBR/ZPV) are out of scope for this
// implementation and are tracked under follow-on stories per Cerner
// deployment.
package cerneradapter

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
	if err := r.Register("cerner", func() spi.EhrAdapter { return New() }); err != nil {
		panic(err)
	}
	return r
}

func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:                   "cerner",
		Vendor:               "Oracle Health (Cerner)",
		Description:          "Adapter for Oracle Health / Cerner Millennium. HL7 v2 ADT/ORU/ORM mapped to FHIR R4 per Millennium R4 IG.",
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

// Lex parses the HL7 v2 bytes via the shared parser and stows the
// resulting *hl7v2.Message in ParsedHL7Message.Segments so Classify and
// MapToFHIR can reuse the parse without reparsing.
func (h *hl7Processor) Lex(raw []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	parsed, err := hl7v2.Parse(cp)
	if err != nil {
		return spi.ParsedHL7Message{Raw: cp}, err
	}
	return spi.ParsedHL7Message{Raw: cp, Segments: parsed}, nil
}

// Classify maps Cerner Open Engine ADT trigger codes to ChangeKind:
//   - A01 (admit), A04 (register), ORM, ORU -> create
//   - A08 (update), A02 (transfer), A11 (cancel admit) -> update
//   - A03 (discharge), A23 (delete), A29 (delete personnel) -> delete
//
// Anything unknown defaults to create so the framework still moves the
// resource through the pipeline rather than dropping it silently.
func (h *hl7Processor) Classify(parsed spi.ParsedHL7Message) (spi.Classification, error) {
	msg := messageFrom(parsed)
	if msg == nil {
		return spi.Classification{Kind: spi.ChangeCreate}, nil
	}
	trigger := strings.ToUpper(msg.TriggerEvent())
	kind := classifyTrigger(trigger)
	return spi.Classification{Kind: kind, CorrelationKey: msg.PatientID()}, nil
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

// MapToFHIR delegates to the shared hl7v2 mapper. If the parsed message is
// missing (e.g. Lex was bypassed in tests), the parser is invoked on the
// raw bytes so MapToFHIR remains usable in isolation.
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
