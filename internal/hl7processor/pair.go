// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// buildPlainChange materializes a [spi.ResourceChange] for the
// non-pairing path (no correlation key, or replacement with no held
// cancellation). LLD §4.3.
//
// Implementation: stub returns a zero value so the unit test fails (RED).
func buildPlainChange(_ classifyExt, _ spi.FhirResource, _ uuid.UUID, _ time.Time) spi.ResourceChange {
	return spi.ResourceChange{}
}

// mergePair builds the merged update [spi.ResourceChange] when both halves
// of a cancel-and-replace pair have arrived. LLD §4.5.
//
// On a same-kind pair (defensive case) returns a non-nil error so the
// caller can fall back to plain emission and log loudly.
//
// Implementation: stub returns a zero value so the unit test fails (RED).
func mergePair(
	_ spi.ChangeKind, _ spi.FhirResource,
	_ spi.ChangeKind, _ spi.FhirResource,
	_ string, _ uuid.UUID, _ time.Time, _ time.Time,
) (spi.ResourceChange, error) {
	return spi.ResourceChange{}, errors.New("not implemented")
}
