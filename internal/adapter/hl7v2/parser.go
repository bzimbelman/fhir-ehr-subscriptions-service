// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package hl7v2 is a small, vendor-neutral HL7 v2 parser and FHIR R4 mapper
// shared by the bundled vendor adapters (cerner, epic, athena, nextgen,
// meditech, allscripts). It implements the message-type coverage demanded by
// OP #168-#173: ADT^A01/A04/A08, ORU^R01, ORM^O01.
//
// Scope: minimum viable mapping. Vendor-specific Z-segments and dialect
// detection are explicitly out of scope per the OP descriptions and are
// covered by follow-on stories. Each vendor adapter wraps this package
// and may layer additional behavior on top.
package hl7v2

import (
	"errors"
	"strings"
)

// Message is a parsed HL7 v2 message: an ordered list of segments plus the
// MSH header that established the encoding characters.
type Message struct {
	segments []Segment
	encoding encodingChars
}

// Segment is a single HL7 v2 segment (e.g. PID, OBR, OBX). Field 0 of a
// segment is the segment ID (e.g. "PID") so callers can index by HL7
// position number directly.
type Segment struct {
	ID     string
	fields []string
	enc    encodingChars
}

// encodingChars carries the per-message separator runes from MSH-1 / MSH-2.
type encodingChars struct {
	field        byte // typically '|'
	component    byte // typically '^'
	repetition   byte // typically '~'
	escape       byte // typically '\\'
	subcomponent byte // typically '&'
}

// ErrEmptyMessage is returned when Parse receives no bytes.
var ErrEmptyMessage = errors.New("hl7v2: empty message")

// ErrMissingMSH is returned when the first segment is not MSH (case-
// insensitive). Some vendor builds emit lowercase "msh"; the parser
// accepts that.
var ErrMissingMSH = errors.New("hl7v2: first segment is not MSH")

// Parse splits raw HL7 v2 bytes into segments. Both \r and \n line endings
// are accepted (some integration engines normalize one to the other). The
// MSH segment is required; encoding characters come from MSH-1/MSH-2.
func Parse(raw []byte) (*Message, error) {
	if len(raw) == 0 {
		return nil, ErrEmptyMessage
	}
	// Normalize line endings so we accept either CR (HL7 spec), LF, or CRLF.
	text := string(raw)
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")

	lines := strings.Split(text, "\r")
	// Drop trailing empties (HL7 messages typically end with a CR).
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, ErrEmptyMessage
	}

	first := lines[0]
	if !strings.EqualFold(safeSegmentID(first), "MSH") {
		return nil, ErrMissingMSH
	}
	if len(first) < 8 {
		return nil, ErrMissingMSH
	}

	enc := encodingChars{
		field:        first[3],
		component:    '^',
		repetition:   '~',
		escape:       '\\',
		subcomponent: '&',
	}
	// MSH-2 carries the remaining encoding chars. Spec order is component,
	// repetition, escape, subcomponent.
	if len(first) >= 8 {
		ec := first[4:8]
		if ec != "" {
			enc.component = ec[0]
		}
		if len(ec) > 1 {
			enc.repetition = ec[1]
		}
		if len(ec) > 2 {
			enc.escape = ec[2]
		}
		if len(ec) > 3 {
			enc.subcomponent = ec[3]
		}
	}

	msg := &Message{encoding: enc}
	for _, line := range lines {
		if line == "" {
			continue
		}
		seg := parseSegment(line, enc)
		msg.segments = append(msg.segments, seg)
	}
	return msg, nil
}

// safeSegmentID returns the first 3 chars (segment id) of a line, or "" if
// the line is shorter than 3 chars.
func safeSegmentID(line string) string {
	if len(line) < 3 {
		return ""
	}
	return strings.ToUpper(line[:3])
}

// parseSegment splits a single segment line into fields. For MSH the
// field separator is itself part of MSH-1, so we splice it back in as
// fields[1] to keep field-number indexing aligned with the HL7 spec.
func parseSegment(line string, enc encodingChars) Segment {
	id := safeSegmentID(line)
	if id == "MSH" {
		// "MSH|^~\\&|...." -> fields = [MSH, |, ^~\&, ...]
		// HL7 spec: MSH-1 is the field separator, MSH-2 is the encoding chars.
		// strings.Split would skip MSH-1, so reconstruct manually.
		body := line[4:] // skip "MSH|"
		parts := strings.Split(body, string(enc.field))
		fields := make([]string, 0, len(parts)+2)
		fields = append(fields, "MSH", string(enc.field))
		fields = append(fields, parts...)
		return Segment{ID: "MSH", fields: fields, enc: enc}
	}
	parts := strings.Split(line, string(enc.field))
	// parts[0] is already the segment id.
	if len(parts) > 0 {
		parts[0] = strings.ToUpper(parts[0])
	}
	return Segment{ID: parts[0], fields: parts, enc: enc}
}

// MessageType returns the MSH-9 value (e.g. "ADT^A01"). When the message
// type is missing, returns "".
func (m *Message) MessageType() string {
	msh, ok := m.Segment("MSH")
	if !ok {
		return ""
	}
	return msh.Field(9)
}

// TriggerEvent returns the trigger code component of MSH-9 (e.g. "A01").
func (m *Message) TriggerEvent() string {
	msh, ok := m.Segment("MSH")
	if !ok {
		return ""
	}
	return msh.Component(9, 2)
}

// MessageCode returns the message code component of MSH-9 (e.g. "ADT").
func (m *Message) MessageCode() string {
	msh, ok := m.Segment("MSH")
	if !ok {
		return ""
	}
	return msh.Component(9, 1)
}

// PatientID returns PID-3.1 — the canonical patient identifier value. Empty
// if no PID segment is present or PID-3 is empty.
func (m *Message) PatientID() string {
	pid, ok := m.Segment("PID")
	if !ok {
		return ""
	}
	if v := pid.Component(3, 1); v != "" {
		return v
	}
	return pid.Field(3)
}

// Segment returns the first segment with the given id (case-insensitive).
func (m *Message) Segment(id string) (Segment, bool) {
	want := strings.ToUpper(id)
	for _, s := range m.segments {
		if s.ID == want {
			return s, true
		}
	}
	return Segment{}, false
}

// Segments returns every segment with the given id, in source order.
func (m *Message) Segments(id string) []Segment {
	want := strings.ToUpper(id)
	var out []Segment
	for _, s := range m.segments {
		if s.ID == want {
			out = append(out, s)
		}
	}
	return out
}

// Field returns the n-th field of the segment using HL7 1-based indexing.
// Returns "" if the field is absent.
func (s Segment) Field(n int) string {
	if n < 0 || n >= len(s.fields) {
		return ""
	}
	return s.fields[n]
}

// Component returns the c-th component (1-based) of field n. Returns ""
// for missing components.
func (s Segment) Component(n, c int) string {
	f := s.Field(n)
	if f == "" {
		return ""
	}
	// Strip repetitions: take the first repetition only.
	if rep := strings.IndexByte(f, s.enc.repetition); rep >= 0 {
		f = f[:rep]
	}
	parts := strings.Split(f, string(s.enc.component))
	if c < 1 || c > len(parts) {
		return ""
	}
	return parts[c-1]
}
