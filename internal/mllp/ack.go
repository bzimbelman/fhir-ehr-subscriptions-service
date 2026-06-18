// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mllp

import (
	"strings"
	"time"
)

// ackCode is the HL7 v2 acknowledgment code.
type ackCode string

const (
	ackAA ackCode = "AA" // application accept
	ackAE ackCode = "AE" // application error (NACK)
	// AR (application reject) is intentionally NOT defined. Malformed-
	// framing dead-letter concerns live downstream of the listener;
	// when framing fails we drop the connection without writing an MSH
	// ACK at all (LLD §8). A never-called AR constant would mislead
	// readers about the listener's actual behavior.
)

// buildACK constructs the framed MLLP ACK bytes for the given inbound MSH.
// The format is:
//
//	MSH|^~\&|<receiver_app>|<receiver_facility>|<sending_app>|<sending_facility>|<timestamp>||ACK|<msh10>|P|2.5
//	MSA|<code>|<msh10>[|<text>]
//
// Wrapped by the standard MLLP framing markers (0x0B ... 0x1C 0x0D).
//
// In v1 the listener does not parse sender/receiver fields off the inbound
// MSH; the ACK uses fixed sender identification ("FHIR_SUBS"/"HOST"). The
// only field that must echo the inbound is MSH-10 (used as the MSA-2
// control id), which is what the EHR correlates against its outbound queue.
func buildACK(code ackCode, msh MSHFields, reasonText string, now time.Time) []byte {
	var b strings.Builder
	b.Grow(180)
	b.WriteByte(frameStart)
	b.WriteString("MSH|^~\\&|FHIR_SUBS|HOST|||")
	b.WriteString(now.UTC().Format("20060102150405"))
	b.WriteString("||ACK|")
	b.WriteString(msh.MessageControlID)
	b.WriteString("|P|2.5")
	b.WriteByte(segmentTerminator)
	b.WriteString("MSA|")
	b.WriteString(string(code))
	b.WriteByte('|')
	b.WriteString(msh.MessageControlID)
	if reasonText != "" && code != ackAA {
		b.WriteByte('|')
		b.WriteString(sanitizeReason(reasonText))
	}
	b.WriteByte(segmentTerminator)
	b.WriteByte(frameEnd1)
	b.WriteByte(frameEnd2)
	return []byte(b.String())
}

// sanitizeReason strips bytes that would corrupt the HL7 framing or the
// MSA segment. Field separators and segment terminators are replaced with
// spaces; anything else is preserved.
func sanitizeReason(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '|', '^', '~', '\\', '&', '\r', '\n':
			b.WriteByte(' ')
		default:
			if r < 0x20 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
