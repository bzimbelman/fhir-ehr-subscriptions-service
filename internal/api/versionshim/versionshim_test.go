// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package versionshim_test

import (
	"errors"
	"testing"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/api/versionshim"
)

func TestNegotiate_DefaultsToR5(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R5 {
		t.Errorf("Negotiate(\"\") = %q; want R5", got)
	}
}

func TestNegotiate_ExplicitR4B(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("application/fhir+json; fhirVersion=4.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R4B {
		t.Errorf("got %q; want R4B", got)
	}
}

func TestNegotiate_ExplicitR5(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("application/fhir+json; fhirVersion=5.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R5 {
		t.Errorf("got %q; want R5", got)
	}
}

func TestNegotiate_InvalidVersion_Returns415(t *testing.T) {
	t.Parallel()
	_, err := versionshim.Negotiate("application/fhir+json; fhirVersion=3.0")
	if err == nil {
		t.Fatal("expected error for unsupported FHIR version, got nil")
	}
	if !errors.Is(err, versionshim.ErrUnsupportedVersion) {
		t.Errorf("err = %v; want ErrUnsupportedVersion", err)
	}
}

func TestNegotiate_WildcardAccept_DefaultsToR5(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("*/*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R5 {
		t.Errorf("got %q; want R5", got)
	}
}

func TestNegotiate_WhitespaceTolerant(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("application/fhir+json;fhirVersion=4.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R4B {
		t.Errorf("got %q; want R4B", got)
	}
}

func TestNegotiate_FhirJSONNoVersion_DefaultsR5(t *testing.T) {
	t.Parallel()
	got, err := versionshim.Negotiate("application/fhir+json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != versionshim.R5 {
		t.Errorf("got %q; want R5", got)
	}
}

func TestVersionString(t *testing.T) {
	t.Parallel()
	if versionshim.R4B.String() != "4.0" {
		t.Errorf("R4B.String() = %q; want 4.0", versionshim.R4B)
	}
	if versionshim.R5.String() != "5.0" {
		t.Errorf("R5.String() = %q; want 5.0", versionshim.R5)
	}
}
