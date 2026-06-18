// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// SHOULD-FIX coverage for S-9 hl7processor items.

package hl7processor

import (
	"errors"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// TestS9_12_VendorLexPanicClassUnexpected — S-9.12: a panic from the
// vendor Lex function was previously bucketed as ErrorClassParse; per
// LLD §9 it should be ErrorClassUnexpected (panics are bugs, not parse
// errors).
func TestS9_12_VendorLexPanicClassUnexpected(t *testing.T) {
	t.Parallel()
	p := panickingLex{}
	_, err := translate(p, []byte("anything"), "")
	if err == nil {
		t.Fatalf("expected error from panicking lex")
	}
	te := asTranslateError(err)
	if te == nil {
		t.Fatalf("error is not a *translateError: %v", err)
	}
	if te.Class != ErrorClassUnexpected {
		t.Errorf("Class got %v want Unexpected", te.Class)
	}
}

// TestS9_12_VendorLexErrorStaysParse — a plain (non-panic) error from
// Lex remains classified as Parse.
func TestS9_12_VendorLexErrorStaysParse(t *testing.T) {
	t.Parallel()
	p := errLex{err: errors.New("malformed segment")}
	_, err := translate(p, []byte("anything"), "")
	te := asTranslateError(err)
	if te == nil {
		t.Fatalf("error is not a *translateError: %v", err)
	}
	if te.Class != ErrorClassParse {
		t.Errorf("Class got %v want Parse", te.Class)
	}
}

type panickingLex struct{ spi.BaseHl7MessageProcessor }

func (panickingLex) Lex([]byte) (spi.ParsedHL7Message, error) {
	panic(errors.New("vendor explosion"))
}

type errLex struct {
	spi.BaseHl7MessageProcessor
	err error
}

func (e errLex) Lex([]byte) (spi.ParsedHL7Message, error) {
	return spi.ParsedHL7Message{}, e.err
}
