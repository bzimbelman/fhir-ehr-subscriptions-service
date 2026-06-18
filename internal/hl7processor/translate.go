// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// # Charset normalization contract
//
// translate consumes the raw HL7 byte slice as-is. It performs NO
// charset transcoding; MSH-18 (Charset) is not consulted. Adapters MUST
// transcode any non-UTF-8 input to UTF-8 BEFORE calling translate so
// downstream JSON marshaling is stable. An adapter that hands UTF-16
// bytes through the framework will produce a parsed message whose
// string fields are mis-interpreted at the resource layer; this is the
// adapter's bug, not the framework's. The contract is enforced at the
// SPI boundary and reaffirmed here for callers reading translate.go in
// isolation (N-1).

// translatedMessage bundles the four-step translation result. Pure data;
// no DB references.
type translatedMessage struct {
	parsed   spi.ParsedHL7Message
	classify spi.Classification
	resource spi.FhirResource
}

// translate runs the four overridable steps in order. Errors and panics
// from vendor code are converted to a typed [*translateError] so the
// caller can dead-letter with the right class. LLD §4.3.
//
// fallbackResourceType is what's stamped on dead-letter records and the
// Update path when [spi.Classification] does not yet carry the type.
func translate(p spi.Hl7MessageProcessor, raw []byte, _ string) (translatedMessage, error) {
	parsed, err := callLex(p, raw)
	if err != nil {
		// S-9.12: a panic in vendor Lex is a bug, not a parse error.
		// Surface it as ErrorClassUnexpected so it routes to the
		// "unparseable" dead-letter bucket but is logged as a vendor
		// bug, not user data.
		if isPanicError(err) {
			return translatedMessage{}, &translateError{Class: ErrorClassUnexpected, Err: err}
		}
		return translatedMessage{}, &translateError{Class: ErrorClassParse, Err: err}
	}

	classified, err := callClassify(p, parsed)
	if err != nil {
		if isPanicError(err) {
			return translatedMessage{}, &translateError{Class: ErrorClassUnexpected, Err: err}
		}
		return translatedMessage{}, &translateError{Class: ErrorClassClassify, Err: err}
	}

	resource, err := callMap(p, parsed, classified)
	if err != nil {
		if isPanicError(err) {
			return translatedMessage{}, &translateError{Class: ErrorClassUnexpected, Err: err}
		}
		return translatedMessage{}, &translateError{Class: ErrorClassMap, Err: err}
	}

	if err := callValidate(p, resource); err != nil {
		return translatedMessage{}, err
	}

	return translatedMessage{parsed: parsed, classify: classified, resource: resource}, nil
}

// vendorPanicError is the marker error type returned by callLex /
// callClassify / callMap when the vendor code panicked. translate
// recognizes it via isPanicError to route to ErrorClassUnexpected
// (S-9.12).
type vendorPanicError struct {
	stage string
	cause any
}

func (e *vendorPanicError) Error() string {
	return fmt.Sprintf("vendor %s panic: %v", e.stage, e.cause)
}

func isPanicError(err error) bool {
	var p *vendorPanicError
	return errors.As(err, &p)
}

// callLex invokes Lex with panic recovery. A panic surfaces as a
// *vendorPanicError so translate can tag it ErrorClassUnexpected per
// LLD §9. Returned errors carry their own class.
func callLex(p spi.Hl7MessageProcessor, raw []byte) (parsed spi.ParsedHL7Message, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &vendorPanicError{stage: "lex", cause: r}
		}
	}()
	return p.Lex(raw)
}

// callClassify invokes Classify with panic recovery.
func callClassify(p spi.Hl7MessageProcessor, parsed spi.ParsedHL7Message) (c spi.Classification, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &vendorPanicError{stage: "classify", cause: r}
		}
	}()
	return p.Classify(parsed)
}

// callMap invokes MapToFHIR with panic recovery.
func callMap(p spi.Hl7MessageProcessor, parsed spi.ParsedHL7Message, c spi.Classification) (r spi.FhirResource, err error) {
	defer func() {
		if r2 := recover(); r2 != nil {
			err = &vendorPanicError{stage: "map", cause: r2}
		}
	}()
	return p.MapToFHIR(parsed, c)
}

// callValidate invokes Validate. A panic becomes a translateError with
// ErrorClassUnexpected; a returned error becomes ErrorClassValidation.
func callValidate(p spi.Hl7MessageProcessor, r spi.FhirResource) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = &translateError{Class: ErrorClassUnexpected, Err: fmt.Errorf("vendor validate panic: %v", rec)}
		}
	}()
	if verr := p.Validate(r); verr != nil {
		return &translateError{Class: ErrorClassValidation, Err: verr}
	}
	return nil
}

// pendingResourceEnvelope is the JSON wrapper around a [spi.FhirResource]
// stored in the encrypted pending_pairs.pending_resource column. The
// schema does not carry resource_type, so we ship it inside the body.
type pendingResourceEnvelope struct {
	ResourceType string `json:"resource_type"`
	ID           string `json:"id,omitempty"`
	Body         []byte `json:"body"`
}

// encodePendingResource serializes a FhirResource for storage.
func encodePendingResource(r spi.FhirResource) ([]byte, error) {
	if r.ResourceType == "" {
		return nil, errors.New("hl7processor: pending resource has empty ResourceType")
	}
	env := pendingResourceEnvelope{
		ResourceType: r.ResourceType,
		ID:           r.ID,
		Body:         r.Body,
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("hl7processor: encode pending resource: %w", err)
	}
	return out, nil
}

// decodePendingResource is the inverse of encodePendingResource.
func decodePendingResource(b []byte) (spi.FhirResource, error) {
	var env pendingResourceEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return spi.FhirResource{}, fmt.Errorf("hl7processor: decode pending resource: %w", err)
	}
	if env.ResourceType == "" {
		return spi.FhirResource{}, errors.New("hl7processor: decoded pending resource has empty ResourceType")
	}
	return spi.FhirResource{
		ResourceType: env.ResourceType,
		ID:           env.ID,
		Body:         env.Body,
	}, nil
}
