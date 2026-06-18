// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"errors"
	"fmt"
)

// segmentTerminator is the HL7 v2 segment terminator (carriage return).
const segmentTerminator = 0x0D

// ErrMalformedMSH is returned when the first segment of a body cannot be
// recognized as a valid MSH segment. The listener still persists the body
// (per the LLD's "extraction is best-effort" rule); callers that care about
// the typed error use it to nack on `allowed_message_types` filters.
var ErrMalformedMSH = errors.New("malformed MSH segment")

// MSHFields holds the only HL7 fields the MLLP listener inspects.
type MSHFields struct {
	// MessageType is the root component of MSH-9 (e.g., "ORU" for "ORU^R01").
	// Empty when MSH-9 is absent.
	MessageType string
	// MessageControlID is MSH-10 verbatim. Empty when MSH-10 is absent.
	MessageControlID string
}

// ExtractMSH reads the first segment of body up to MSH-9 and MSH-10. It does
// no structure-aware parsing of the rest of the message. Per the LLD, this
// is best-effort: a malformed MSH returns ErrMalformedMSH, but the listener
// caller may still persist the body.
func ExtractMSH(body []byte) (MSHFields, error) {
	if len(body) == 0 {
		return MSHFields{}, fmt.Errorf("%w: empty body", ErrMalformedMSH)
	}

	// First segment is everything up to the first 0x0D (or the entire body).
	first := body
	for i, b := range body {
		if b == segmentTerminator {
			first = body[:i]
			break
		}
	}

	// Must begin with literal "MSH" plus a field separator byte at index 3.
	if len(first) < 4 {
		return MSHFields{}, fmt.Errorf("%w: first segment too short (%d bytes)", ErrMalformedMSH, len(first))
	}
	if first[0] != 'M' || first[1] != 'S' || first[2] != 'H' {
		return MSHFields{}, fmt.Errorf("%w: first segment is not MSH", ErrMalformedMSH)
	}
	sep := first[3]

	// Per HL7 v2, MSH-1 is the field separator itself. Tokenize from index 3
	// (inclusive of the separator), so fieldSlice[0] is empty (between MSH and
	// the first separator), fieldSlice[1] is MSH-2 (encoding chars), etc.
	// Field n in HL7 numbering corresponds to fieldSlice[n-1].
	rest := first[3:]
	// Walk the rest segmenting by sep.
	// We only need fields up to MSH-10 (slice index 9), so cap at 11 fields.
	const maxFields = 11
	fields := make([][]byte, 0, maxFields)
	start := 0
	for i := 0; i < len(rest); i++ {
		if rest[i] == sep {
			fields = append(fields, rest[start:i])
			start = i + 1
			if len(fields) >= maxFields {
				break
			}
		}
	}
	if len(fields) < maxFields {
		fields = append(fields, rest[start:])
	}

	out := MSHFields{}
	if len(fields) > 8 {
		// MSH-9 is fields[8]. Root type is the first ^-component.
		raw := fields[8]
		root := raw
		for i, b := range raw {
			if b == '^' {
				root = raw[:i]
				break
			}
		}
		out.MessageType = string(root)
	}
	if len(fields) > 9 {
		out.MessageControlID = string(fields[9])
	}

	return out, nil
}
