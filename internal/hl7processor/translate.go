// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package hl7processor

import (
	"errors"

	"github.com/fhir-subscriptions-foss/fhir-subs/internal/adapter/spi"
)

// translatedMessage bundles the four-step translation result. Pure data;
// no DB references.
type translatedMessage struct {
	parsed   spi.ParsedHL7Message
	classify spi.Classification
	resource spi.FhirResource
}

// translate runs the four overridable steps in order. Errors and panics
// from vendor code are converted to a typed [*translateError] so the
// caller can dead-letter with the right class.
//
// Implementation: stub. RED.
func translate(_ spi.Hl7MessageProcessor, _ []byte, _ string) (translatedMessage, error) {
	return translatedMessage{}, errors.New("not implemented")
}

// encodePendingResource is the on-disk serialization for
// pending_pairs.pending_resource. Implementation: stub. RED.
func encodePendingResource(_ spi.FhirResource) ([]byte, error) {
	return nil, errors.New("not implemented")
}

// decodePendingResource is the inverse of encodePendingResource.
// Implementation: stub. RED.
func decodePendingResource(_ []byte) (spi.FhirResource, error) {
	return spi.FhirResource{}, errors.New("not implemented")
}
