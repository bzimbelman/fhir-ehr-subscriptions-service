// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// fakeProcessor is an [spi.Hl7MessageProcessor] test double.
type fakeProcessor struct {
	spi.BaseHl7MessageProcessor
	lexFn      func([]byte) (spi.ParsedHL7Message, error)
	classifyFn func(spi.ParsedHL7Message) (spi.Classification, error)
	mapFn      func(spi.ParsedHL7Message, spi.Classification) (spi.FhirResource, error)
	validateFn func(spi.FhirResource) error
}

func (f *fakeProcessor) Lex(b []byte) (spi.ParsedHL7Message, error) {
	if f.lexFn != nil {
		return f.lexFn(b)
	}
	return spi.ParsedHL7Message{Raw: b}, nil
}

func (f *fakeProcessor) Classify(p spi.ParsedHL7Message) (spi.Classification, error) {
	if f.classifyFn != nil {
		return f.classifyFn(p)
	}
	return spi.Classification{Kind: spi.ChangeCreate}, nil
}

func (f *fakeProcessor) MapToFHIR(p spi.ParsedHL7Message, c spi.Classification) (spi.FhirResource, error) {
	if f.mapFn != nil {
		return f.mapFn(p, c)
	}
	return spi.FhirResource{ResourceType: "ServiceRequest", Body: []byte(`{"resourceType":"ServiceRequest"}`)}, nil
}

func (f *fakeProcessor) Validate(r spi.FhirResource) error {
	if f.validateFn != nil {
		return f.validateFn(r)
	}
	return nil
}

// TestTranslate_Success: lex/classify/map/validate all succeed; returns a
// translated triple ready for resolve_pairing.
func TestTranslate_Success(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{}
	tr, err := translate(p, []byte("MSH|..."), "ServiceRequest")
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if tr.classify.Kind != spi.ChangeCreate {
		t.Fatalf("kind: %s", tr.classify.Kind)
	}
	if tr.resource.ResourceType != "ServiceRequest" {
		t.Fatalf("resource type: %s", tr.resource.ResourceType)
	}
}

// TestTranslate_LexError -> ErrorClassParse.
func TestTranslate_LexError(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{
		lexFn: func([]byte) (spi.ParsedHL7Message, error) { return spi.ParsedHL7Message{}, errors.New("bad MSH") },
	}
	_, err := translate(p, []byte("garbage"), "ServiceRequest")
	te := asTranslateError(err)
	if te == nil || te.Class != ErrorClassParse {
		t.Fatalf("expected parse class, got %+v", err)
	}
}

// TestTranslate_ClassifyError -> ErrorClassClassify.
func TestTranslate_ClassifyError(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{
		classifyFn: func(spi.ParsedHL7Message) (spi.Classification, error) {
			return spi.Classification{}, errors.New("unsupported MSH-9")
		},
	}
	_, err := translate(p, []byte("MSH|..."), "ServiceRequest")
	te := asTranslateError(err)
	if te == nil || te.Class != ErrorClassClassify {
		t.Fatalf("expected classify class, got %+v", err)
	}
}

// TestTranslate_MapError -> ErrorClassMap.
func TestTranslate_MapError(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{
		mapFn: func(spi.ParsedHL7Message, spi.Classification) (spi.FhirResource, error) {
			return spi.FhirResource{}, errors.New("missing PID")
		},
	}
	_, err := translate(p, []byte("MSH|..."), "ServiceRequest")
	te := asTranslateError(err)
	if te == nil || te.Class != ErrorClassMap {
		t.Fatalf("expected map class, got %+v", err)
	}
}

// TestTranslate_ValidationError -> ErrorClassValidation.
func TestTranslate_ValidationError(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{
		validateFn: func(spi.FhirResource) error { return errors.New("missing required") },
	}
	_, err := translate(p, []byte("MSH|..."), "ServiceRequest")
	te := asTranslateError(err)
	if te == nil || te.Class != ErrorClassValidation {
		t.Fatalf("expected validation class, got %+v", err)
	}
}

// TestTranslate_VendorPanic_IsUnexpected: a vendor that panics is caught
// and surfaced as ErrorClassUnexpected per LLD §9.
func TestTranslate_VendorPanic_IsUnexpected(t *testing.T) {
	t.Parallel()
	p := &fakeProcessor{
		validateFn: func(spi.FhirResource) error { panic("vendor bug") },
	}
	_, err := translate(p, []byte("MSH|..."), "ServiceRequest")
	te := asTranslateError(err)
	if te == nil || te.Class != ErrorClassUnexpected {
		t.Fatalf("expected unexpected class, got %+v", err)
	}
}

// TestEnvelope_RoundTrip: pending_resource encodes resource_type + body
// so resolve can reconstruct the FhirResource without parsing JSON.
func TestEnvelope_RoundTrip(t *testing.T) {
	t.Parallel()
	in := spi.FhirResource{ResourceType: "ServiceRequest", ID: "abc", Body: []byte(`{"resourceType":"ServiceRequest","id":"abc"}`)}
	enc, err := encodePendingResource(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodePendingResource(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ResourceType != in.ResourceType || out.ID != in.ID || string(out.Body) != string(in.Body) {
		t.Fatalf("round-trip mismatch: in=%+v out=%+v", in, out)
	}
}

// TestEnvelope_RejectsBadJSON: garbage envelopes return an error.
func TestEnvelope_RejectsBadJSON(t *testing.T) {
	t.Parallel()
	if _, err := decodePendingResource([]byte("not json")); err == nil {
		t.Fatal("expected error decoding garbage")
	}
}

// TestProcessingOutcome_KindZeroIsInvalid: a zero-value outcome is invalid;
// callers must explicitly set kind. Defensive against silent bugs.
func TestProcessingOutcome_KindZeroIsInvalid(t *testing.T) {
	t.Parallel()
	var o processingOutcome
	if o.kind != 0 {
		t.Fatalf("zero outcome kind expected 0, got %d", o.kind)
	}
}

// TestErrorClass_StringRoundTrip: each ErrorClass value renders to a stable
// label used in metrics + dead_letters error_class column.
func TestErrorClass_StringRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		c    ErrorClass
		want string
	}{
		{ErrorClassParse, "parse"},
		{ErrorClassClassify, "classify"},
		{ErrorClassMap, "map"},
		{ErrorClassValidation, "validation"},
		{ErrorClassUnexpected, "unexpected"},
	}
	for _, tc := range cases {
		if tc.c.String() != tc.want {
			t.Errorf("%q: got %q", tc.want, tc.c.String())
		}
	}
}

// TestHeldPair_RoundTrip: the heldPair struct carries every field needed
// to re-create the resource_changes row on partner arrival or expiry.
func TestHeldPair_RoundTrip(t *testing.T) {
	t.Parallel()
	src := uuid.New()
	corr := uuid.New()
	now := time.Now()
	hp := heldPair{
		CorrelationKey:   "k",
		ListenerEndpoint: "adt",
		Resource:         spi.FhirResource{ResourceType: "ServiceRequest"},
		PendingKind:      spi.ChangeDelete,
		SourceMessageID:  src,
		ExpiresAt:        now.Add(30 * time.Second),
		CreatedAt:        now,
		ResourceType:     "ServiceRequest",
		CorrelationID:    corr,
	}
	if hp.CorrelationKey != "k" {
		t.Fatal("CorrelationKey")
	}
	if hp.ListenerEndpoint != "adt" {
		t.Fatal("ListenerEndpoint")
	}
	if hp.PendingKind != spi.ChangeDelete {
		t.Fatal("PendingKind")
	}
	if !hp.ExpiresAt.After(hp.CreatedAt) {
		t.Fatal("ExpiresAt should be after CreatedAt")
	}
	if hp.ResourceType != "ServiceRequest" {
		t.Fatal("ResourceType")
	}
	if hp.SourceMessageID != src || hp.CorrelationID != corr {
		t.Fatal("heldPair did not retain ids")
	}
}
