// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package epicadapter is the P3.2 SPI scaffold for Epic Systems. It declares
// an Epic-specific manifest and constructs a passthrough Hl7MessageProcessor
// whose Lex / Classify / MapToFHIR delegate to the framework defaults until a
// real Z-segment + Interconnect FHIR mapping is contributed.
//
// The point of this stub is to prove the SPI is a genuine plug-in surface:
// every interface is implementable from outside the default adapter, the
// registry can load us, and the manifest validates. Real translation
// (HL7 v2.5+ Z-segments per Epic's chapter "Bridges Specification" / Z*Note,
// Interconnect FHIR R4 Patient/Encounter/Observation profiles) is intentionally
// left as a TODO so vendor SMEs can pick it up without disturbing scaffolding.
package epicadapter

import (
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// Adapter is the Epic Systems EHR adapter scaffold.
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

// Manifest declares Epic-specific identifiers. SupportedEhrVersions is "*"
// until version-pinned profiles are contributed.
func (a *Adapter) Manifest() spi.AdapterManifest {
	return spi.AdapterManifest{
		ID:          "epic",
		Vendor:      "Epic Systems",
		Description: "Scaffold adapter for Epic Systems (P3.2). HL7 v2 Z-segments and Interconnect FHIR profile mapping are TODO.",
		// TODO(epic): pin to ">=2.5" once Z-segment lex covers Bridges Spec.
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

// BuildHl7Processor returns a passthrough processor. TODO(epic): replace with
// a Z-segment-aware lex + ORU/ADT classification per Epic Bridges Specification
// (HL7 v2.5+ Z*Notes, Z-segments for problem and result detail).
func (a *Adapter) BuildHl7Processor(_ spi.AdapterContext) spi.Hl7MessageProcessor {
	return &hl7Processor{}
}

// BuildFhirScanRunner returns nil — capability is not declared. Wiring an
// Interconnect-FHIR R4 scan plan is a separate follow-up.
func (a *Adapter) BuildFhirScanRunner(_ spi.AdapterContext) spi.FhirScanRunner { return nil }

// BuildVendorAPIClient returns nil — Epic Interconnect feed integration is TODO.
func (a *Adapter) BuildVendorAPIClient(_ spi.AdapterContext) spi.VendorAPIClient { return nil }

// BuildHydrationService returns nil — capability is not declared.
func (a *Adapter) BuildHydrationService(_ spi.AdapterContext) spi.HydrationService { return nil }

// hl7Processor is a TODO passthrough. Real Epic mapping needs:
//   - Z-segment parsing (Z*Notes / ZBR / ZPV / Bridges-Spec custom segments)
//   - ORM/ORU/ADT trigger-event handling per HL7 v2.5+ Chapter 4 + Epic spec
//   - FHIR R4 Patient/Encounter/Observation/Procedure mapping per Interconnect
//     profiles
type hl7Processor struct {
	spi.BaseHl7MessageProcessor
}

func (h *hl7Processor) Lex(raw []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return spi.ParsedHL7Message{Raw: cp, Segments: nil}, nil
}

func (h *hl7Processor) Classify(_ spi.ParsedHL7Message) (spi.Classification, error) {
	return spi.Classification{Kind: spi.ChangeCreate, CorrelationKey: ""}, nil
}

func (h *hl7Processor) MapToFHIR(_ spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	body := []byte(`{"resourceType":"Bundle","type":"collection"}`)
	return spi.FhirResource{ResourceType: "Bundle", Body: body}, nil
}
