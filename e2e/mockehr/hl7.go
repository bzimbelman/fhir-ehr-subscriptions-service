// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

package mockehr

import (
	"fmt"
	"strings"
	"time"
)

// HL7 v2 message builders for the trigger events the test harness drives.
//
// These are not a general-purpose HL7 v2 library. They produce the minimal,
// realistic ER7 segment shape each scenario type needs:
//   * the MSH header that the MLLP listener parses for MSH-9 and MSH-10;
//   * the canonical event-bearing segments (ORC, OBR, OBX, SCH, TXA);
//   * a stable PID segment so tests can assert patient identity.
//
// They are deliberately strict about wire shape: `|^~\&` encoding chars,
// `\r` segment terminators, no MLLP framing bytes (the framing is added by
// the MLLP server, not the message builder).
//
// Default sending application/facility values are constants so tests can
// match on them without coordinating across files.

const (
	defaultSendingApp      = "MOCKEHR"
	defaultSendingFacility = "E2E"
	defaultReceivingApp    = "FHIRSUBS"
	defaultReceiving       = "TEST"
	defaultProcessingID    = "T" // Test
	defaultVersionID       = "2.5.1"

	segTerm = "\r"
)

// ORC-1 control codes used by the harness.
const (
	ORCControlNew     = "NW"
	ORCControlCancel  = "CA"
	ORCControlReplace = "RP"
)

// ADTOptions parameterizes BuildADT. Every field has a sensible default
// when zero so smoke tests can fill in just the fields they care about.
type ADTOptions struct {
	TriggerEvent  string // e.g., "A01"; required.
	MessageID     string // MSH-10; required.
	PatientID     string // PID-3 / MRN; required.
	PatientFamily string
	PatientGiven  string
	Now           time.Time // optional fixed clock for golden tests
}

// BuildADT returns an HL7 v2.5.1 ADT message with MSH, EVN, and PID.
func BuildADT(o ADTOptions) string {
	if o.TriggerEvent == "" {
		o.TriggerEvent = "A01"
	}
	now := timeOrNow(o.Now)
	msh := buildMSH("ADT^"+o.TriggerEvent, o.MessageID, now)
	evn := fmt.Sprintf("EVN|%s|%s", o.TriggerEvent, fmtTS(now))
	family := orDefault(o.PatientFamily, "Doe")
	given := orDefault(o.PatientGiven, "Jane")
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||%s^%s", o.PatientID, defaultSendingFacility, family, given)
	return joinSegments(msh, evn, pid)
}

// ORMOptions parameterizes BuildORM (orders). The cancel-and-replacement
// pair invariant is the caller's responsibility: pass the same
// PlacerOrderID and FillerOrderID for both halves of the pair.
type ORMOptions struct {
	ControlCode    string // ORC-1: NW / CA / RP.
	MessageID      string // MSH-10.
	PlacerOrderID  string // ORC-2.
	FillerOrderID  string // ORC-3.
	PatientID      string
	UniversalSvcID string // OBR-4, e.g. "CBC^Complete Blood Count^L"
	Now            time.Time
}

// BuildORM returns a minimal ORM^O01 message (MSH, PID, ORC, OBR).
func BuildORM(o ORMOptions) string {
	if o.ControlCode == "" {
		o.ControlCode = ORCControlNew
	}
	now := timeOrNow(o.Now)
	msh := buildMSH("ORM^O01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", orDefault(o.PatientID, "MRN0"), defaultSendingFacility)
	orc := fmt.Sprintf("ORC|%s|%s|%s|||%s", o.ControlCode, o.PlacerOrderID, o.FillerOrderID, fmtTS(now))
	universal := orDefault(o.UniversalSvcID, "TEST^Test Order^L")
	obr := fmt.Sprintf("OBR|1|%s|%s|%s|||%s", o.PlacerOrderID, o.FillerOrderID, universal, fmtTS(now))
	return joinSegments(msh, pid, orc, obr)
}

// ORUOptions parameterizes BuildORU (lab results).
type ORUOptions struct {
	MessageID string
	PatientID string
	Result    ORUResult
	Now       time.Time
}

// ORUResult is one OBX segment's worth of result data.
type ORUResult struct {
	ObservationID string // e.g., "GLU^Glucose^L"
	Value         string
	Unit          string
	RefRange      string
	AbnormalFlag  string
}

// BuildORU returns a minimal ORU^R01 message (MSH, PID, OBR, OBX).
func BuildORU(o ORUOptions) string {
	now := timeOrNow(o.Now)
	msh := buildMSH("ORU^R01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", o.PatientID, defaultSendingFacility)
	obr := fmt.Sprintf("OBR|1|||%s|||%s", o.Result.ObservationID, fmtTS(now))
	obx := fmt.Sprintf("OBX|1|NM|%s||%s|%s|%s|%s|||F",
		o.Result.ObservationID,
		o.Result.Value,
		o.Result.Unit,
		o.Result.RefRange,
		o.Result.AbnormalFlag,
	)
	return joinSegments(msh, pid, obr, obx)
}

// SIUOptions parameterizes BuildSIU (scheduling).
type SIUOptions struct {
	TriggerEvent string // e.g., "S12"
	MessageID    string
	PatientID    string
	ApptID       string
	Now          time.Time
}

// BuildSIU returns a minimal SIU message (MSH, SCH, PID).
func BuildSIU(o SIUOptions) string {
	now := timeOrNow(o.Now)
	msh := buildMSH("SIU^"+orDefault(o.TriggerEvent, "S12"), o.MessageID, now)
	sch := fmt.Sprintf("SCH|%s|||||%s", o.ApptID, fmtTS(now))
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", o.PatientID, defaultSendingFacility)
	return joinSegments(msh, sch, pid)
}

// MDMOptions parameterizes BuildMDM (clinical document notification).
type MDMOptions struct {
	TriggerEvent string // e.g., "T02"
	MessageID    string
	PatientID    string
	DocType      string
	DocID        string
	Now          time.Time
}

// BuildMDM returns a minimal MDM message (MSH, EVN, PID, TXA).
func BuildMDM(o MDMOptions) string {
	now := timeOrNow(o.Now)
	msh := buildMSH("MDM^"+orDefault(o.TriggerEvent, "T02"), o.MessageID, now)
	evn := fmt.Sprintf("EVN|%s|%s", orDefault(o.TriggerEvent, "T02"), fmtTS(now))
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", o.PatientID, defaultSendingFacility)
	txa := fmt.Sprintf("TXA|1|%s||%s|%s", orDefault(o.DocType, "CN"), fmtTS(now), o.DocID)
	return joinSegments(msh, evn, pid, txa)
}

// buildMSH composes an MSH segment with the project defaults. MSH-9 is the
// pre-formatted trigger string (e.g., "ADT^A01"); MSH-10 is the message
// control id; MSH-7 is the message timestamp.
func buildMSH(messageType, controlID string, now time.Time) string {
	return strings.Join([]string{
		"MSH",
		"^~\\&",
		defaultSendingApp,
		defaultSendingFacility,
		defaultReceivingApp,
		defaultReceiving,
		fmtTS(now),
		"",
		messageType,
		controlID,
		defaultProcessingID,
		defaultVersionID,
	}, "|")
}

func joinSegments(segs ...string) string {
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString(s)
		sb.WriteString(segTerm)
	}
	return sb.String()
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func timeOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// fmtTS formats a time in HL7 v2's TS format `YYYYMMDDHHmmSS`.
func fmtTS(t time.Time) string {
	return t.UTC().Format("20060102150405")
}
