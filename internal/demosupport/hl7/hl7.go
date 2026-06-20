// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package hl7 builds the minimal HL7 v2 messages the demo CLIs need.
// It exists (per OP #158) so cmd/demo-publisher does not depend on
// e2e/mockehr — operator-facing binaries must not import test
// scaffolding. The subset here is the demo's: ADT^A01 and ORU^R01.
package hl7

import (
	"fmt"
	"strings"
	"time"
)

// HL7 v2 wire constants. Same values as the e2e mock so message bytes
// remain interchangeable between contexts.
const (
	defaultSendingApp      = "MOCKEHR"
	defaultSendingFacility = "E2E"
	defaultReceivingApp    = "FHIRSUBS"
	defaultReceiving       = "TEST"
	defaultProcessingID    = "T"
	defaultVersionID       = "2.5.1"

	segTerm = "\r"
)

// ADTOptions parameterizes BuildADT.
type ADTOptions struct {
	TriggerEvent  string
	MessageID     string
	PatientID     string
	PatientFamily string
	PatientGiven  string
	Now           time.Time
}

// BuildADT returns an HL7 v2.5.1 ADT message (MSH, EVN, PID).
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

// ORUOptions parameterizes BuildORU.
type ORUOptions struct {
	MessageID string
	PatientID string
	Result    ORUResult
	Now       time.Time
}

// ORUResult is one OBX segment's worth of result data.
type ORUResult struct {
	ObservationID string
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

func fmtTS(t time.Time) string {
	return t.UTC().Format("20060102150405")
}
