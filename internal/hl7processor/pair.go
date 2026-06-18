// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

// errSameKindPair is returned by [mergePair] when both halves under a
// correlation key have the same change kind. The caller falls back to
// plain emission for the arriving message and logs the condition.
var errSameKindPair = fmt.Errorf("hl7processor: same-kind pair under one correlation key")

// buildPlainChange materializes a [spi.ResourceChange] for the
// non-pairing path (no correlation key, or replacement with no held
// cancellation). LLD §4.3 / §4.5.
func buildPlainChange(c classifyExt, resource spi.FhirResource, correlationID uuid.UUID, occurredAt time.Time) spi.ResourceChange {
	rt := c.ResourceType
	if rt == "" {
		rt = resource.ResourceType
	}
	return spi.ResourceChange{
		ResourceType:  rt,
		ChangeKind:    c.Kind,
		Resource:      resource,
		OccurredAt:    occurredAt,
		CorrelationID: correlationID,
	}
}

// mergePair builds the merged update [spi.ResourceChange] when both halves
// of a cancel-and-replace pair have arrived. LLD §4.5.
//
// Held kind plus arriving kind:
//   - (Delete, Create) — held cancellation, arriving replacement.
//   - (Create, Delete) — held replacement, arriving cancellation.
//
// Both produce change_kind=update with cancellation as previous_resource
// and replacement as resource.
//
// Same-kind combinations return [errSameKindPair].
//
// `correlationID` is the held half's correlation id so retries see a stable
// id (ADR 0008 #2). `occurredAt` is the merged occurred_at — caller is
// expected to pass max(held_created_at, parsed_msh7_or_now).
func mergePair(
	heldKind spi.ChangeKind, heldResource spi.FhirResource,
	arrivingKind spi.ChangeKind, arrivingResource spi.FhirResource,
	resourceType string, correlationID uuid.UUID, _ time.Time, occurredAt time.Time,
) (spi.ResourceChange, error) {
	var previous, current spi.FhirResource
	switch {
	case heldKind == spi.ChangeDelete && arrivingKind == spi.ChangeCreate:
		previous, current = heldResource, arrivingResource
	case heldKind == spi.ChangeCreate && arrivingKind == spi.ChangeDelete:
		previous, current = arrivingResource, heldResource
	default:
		return spi.ResourceChange{}, errSameKindPair
	}

	prev := previous
	return spi.ResourceChange{
		ResourceType:     resourceType,
		ChangeKind:       spi.ChangeUpdate,
		Resource:         current,
		PreviousResource: &prev,
		OccurredAt:       occurredAt,
		CorrelationID:    correlationID,
	}, nil
}
