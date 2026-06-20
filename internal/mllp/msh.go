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
	// MessageDateTime is MSH-7 verbatim — the message date/time as the
	// EHR stamped it. Empty when MSH-7 is absent. Surfaced so the
	// HL7 processor can record `occurred = MSH-7` instead of
	// "now()" (S-9.10).
	MessageDateTime string
	// Charset is MSH-18 verbatim — the message character set as
	// declared by the sender (e.g., "UNICODE UTF-8"). Empty when
	// MSH-18 is absent. Surfaced so callers can log / metric the
	// encoding rather than silently treating non-ASCII content as
	// ASCII (S-9.5).
	Charset string
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

	// Must begin with "MSH" (case-insensitive — Allscripts pre-2014 and
	// MEDITECH MAGIC emit lowercase "msh") plus a field separator byte
	// at index 3.
	if len(first) < 4 {
		return MSHFields{}, fmt.Errorf("%w: first segment too short (%d bytes)", ErrMalformedMSH, len(first))
	}
	if !(first[0] == 'M' || first[0] == 'm') ||
		!(first[1] == 'S' || first[1] == 's') ||
		!(first[2] == 'H' || first[2] == 'h') {
		return MSHFields{}, fmt.Errorf("%w: first segment is not MSH", ErrMalformedMSH)
	}
	sep := first[3]
	// MSH-2 (the encoding-characters field) carries component, repetition,
	// escape, subcomponent in that order. Default escape is '\'. The
	// field walker honors the escape byte so that an escaped pipe inside
	// e.g. MSH-4 ("FAC\|WITHPIPE") does not drift field counting (OP #194).
	esc := byte('\\')
	if len(first) >= 8 {
		esc = first[6]
	}

	// Per HL7 v2, MSH-1 is the field separator itself. Tokenize from index 3
	// (inclusive of the separator), so fieldSlice[0] is empty (between MSH and
	// the first separator), fieldSlice[1] is MSH-2 (encoding chars), etc.
	// Field n in HL7 numbering corresponds to fieldSlice[n-1].
	rest := first[3:]
	// Walk the rest segmenting by sep.
	// We need fields up to MSH-18 (slice index 17), so cap at 19 fields.
	const maxFields = 19
	fields := make([][]byte, 0, maxFields)
	start := 0
	for i := 0; i < len(rest); i++ {
		// Honor MSH-2 escape: an escape byte and the byte that follows
		// it are opaque field content. Skip both.
		if rest[i] == esc && i+1 < len(rest) {
			i++
			continue
		}
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
	if len(fields) > 6 {
		out.MessageDateTime = string(fields[6])
	}
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
	if len(fields) > 17 {
		out.Charset = string(fields[17])
	}

	return out, nil
}
