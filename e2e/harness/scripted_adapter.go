// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// ScriptedAdapter is a tiny test SPI implementation that lets a
// scenario inject a fixed FHIR body and classification per inbound HL7
// message. The body shapes are how the matcher's resource-trigger
// evaluator and the submatcher's filterBy evaluator see the world, so a
// scenario that wants to exercise filterBy decisions or topic
// matching needs control over the body — not the raw passthrough body
// the defaultadapter emits.
type ScriptedAdapter struct {
	spi.BaseHl7MessageProcessor

	// ResourceType is the FHIR resource type the topic catalog and
	// matcher key off (e.g., "ServiceRequest", "Patient", "Observation").
	ResourceType string

	// BodyFn returns the body for a given raw HL7 message. Tests inject
	// per-message variation here (e.g., to flip status=active vs
	// status=cancelled across two messages).
	BodyFn func(raw []byte) []byte

	// ClassifyFn returns the classification for a given message. If
	// nil, every message classifies as ChangeCreate with no
	// correlation key.
	ClassifyFn func(raw []byte) spi.Classification
}

// Lex preserves raw bytes verbatim.
func (s *ScriptedAdapter) Lex(b []byte) (spi.ParsedHL7Message, error) {
	cp := make([]byte, len(b))
	copy(cp, b)
	return spi.ParsedHL7Message{Raw: cp}, nil
}

// Classify routes through ClassifyFn or returns a vanilla create.
func (s *ScriptedAdapter) Classify(p spi.ParsedHL7Message) (spi.Classification, error) {
	if s.ClassifyFn != nil {
		return s.ClassifyFn(p.Raw), nil
	}
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}

// MapToFHIR builds a FhirResource using ResourceType and BodyFn.
func (s *ScriptedAdapter) MapToFHIR(p spi.ParsedHL7Message, _ spi.Classification) (spi.FhirResource, error) {
	rt := s.ResourceType
	if rt == "" {
		rt = "Bundle"
	}
	body := []byte(`{"resourceType":"` + rt + `"}`)
	if s.BodyFn != nil {
		body = s.BodyFn(p.Raw)
	}
	return spi.FhirResource{ResourceType: rt, Body: body}, nil
}

// Validate returns nil — scenarios that want to test the validation
// dead-letter path swap this out for a wrapper.
func (s *ScriptedAdapter) Validate(_ spi.FhirResource) error { return nil }
