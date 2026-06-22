// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// HL7 v2 builders for the MLLP control plane subsystem. Lifted
// unchanged in behaviour from cmd/test-mllp-control-plane/main.go.
package main

import (
	"fmt"
	"strings"
	"time"
)

// HL7 v2 builder constants.
const (
	defaultSendingApp      = "MOCKEHR"
	defaultSendingFacility = "E2E"
	defaultReceivingApp    = "FHIRSUBS"
	defaultReceiving       = "TEST"
	defaultProcessingID    = "T"
	defaultVersionID       = "2.5.1"
	segTerm                = "\r"
)

type adtOptions struct {
	TriggerEvent  string
	MessageID     string
	PatientID     string
	PatientFamily string
	PatientGiven  string
}

func buildADT(o adtOptions) string {
	if o.TriggerEvent == "" {
		o.TriggerEvent = "A01"
	}
	now := time.Now().UTC()
	msh := buildMSH("ADT^"+o.TriggerEvent, o.MessageID, now)
	evn := fmt.Sprintf("EVN|%s|%s", o.TriggerEvent, fmtTS(now))
	family := orDefault(o.PatientFamily, "Doe")
	given := orDefault(o.PatientGiven, "Jane")
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||%s^%s", o.PatientID, defaultSendingFacility, family, given)
	return joinSegments(msh, evn, pid)
}

type ormOptions struct {
	ControlCode    string
	MessageID      string
	PlacerOrderID  string
	FillerOrderID  string
	PatientID      string
	UniversalSvcID string
}

func buildORM(o ormOptions) string {
	if o.ControlCode == "" {
		o.ControlCode = "NW"
	}
	now := time.Now().UTC()
	msh := buildMSH("ORM^O01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", orDefault(o.PatientID, "MRN0"), defaultSendingFacility)
	orc := fmt.Sprintf("ORC|%s|%s|%s|||%s", o.ControlCode, o.PlacerOrderID, o.FillerOrderID, fmtTS(now))
	universal := orDefault(o.UniversalSvcID, "TEST^Test Order^L")
	obr := fmt.Sprintf("OBR|1|%s|%s|%s|||%s", o.PlacerOrderID, o.FillerOrderID, universal, fmtTS(now))
	return joinSegments(msh, pid, orc, obr)
}

type oruOptions struct {
	MessageID     string
	PatientID     string
	ObservationID string
	Value         string
	Unit          string
	RefRange      string
	AbnormalFlag  string
}

func buildORU(o oruOptions) string {
	now := time.Now().UTC()
	msh := buildMSH("ORU^R01", o.MessageID, now)
	pid := fmt.Sprintf("PID|1||%s^^^%s^MR||Doe^Jane", o.PatientID, defaultSendingFacility)
	obr := fmt.Sprintf("OBR|1|||%s|||%s", o.ObservationID, fmtTS(now))
	obx := fmt.Sprintf("OBX|1|NM|%s||%s|%s|%s|%s|||F",
		o.ObservationID, o.Value, o.Unit, o.RefRange, o.AbnormalFlag)
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

func fmtTS(t time.Time) string {
	return t.UTC().Format("20060102150405")
}
