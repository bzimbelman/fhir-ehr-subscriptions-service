// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestBuildMessage_ORUR01_HasMSHAndPatient(t *testing.T) {
	t.Parallel()
	entry := MessageEntry{
		Template: "oru-r01",
		Fields: map[string]string{
			"patient_id":       "ABC123",
			"observation_code": "718-7",
			"value":            "13.5",
			"unit":             "g/dL",
		},
	}
	body, ctrlID, err := buildMessage(entry, "MSG0001")
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	if ctrlID != "MSG0001" {
		t.Fatalf("control id: got %q want MSG0001", ctrlID)
	}
	if !strings.HasPrefix(body, "MSH|^~\\&|") {
		t.Fatalf("expected MSH header prefix, got: %q", body[:min(len(body), 40)])
	}
	if !strings.Contains(body, "ORU^R01") {
		t.Fatalf("expected ORU^R01 in MSH-9; body: %q", body)
	}
	if !strings.Contains(body, "ABC123") {
		t.Fatalf("expected patient ABC123 in body: %q", body)
	}
	if !strings.Contains(body, "718-7") {
		t.Fatalf("expected observation_code 718-7 in body: %q", body)
	}
	if !strings.Contains(body, "13.5") {
		t.Fatalf("expected value 13.5 in body: %q", body)
	}
	if !strings.Contains(body, "MSG0001") {
		t.Fatalf("expected control id MSG0001 in body: %q", body)
	}
}

func TestBuildMessage_ADTA01_HasMSHAndPatient(t *testing.T) {
	t.Parallel()
	entry := MessageEntry{
		Template: "adt-a01",
		Fields: map[string]string{
			"patient_id": "ABC123",
		},
	}
	body, ctrlID, err := buildMessage(entry, "ADT-001")
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	if ctrlID != "ADT-001" {
		t.Fatalf("control id: got %q", ctrlID)
	}
	if !strings.Contains(body, "ADT^A01") {
		t.Fatalf("expected ADT^A01 in MSH-9; body: %q", body)
	}
	if !strings.Contains(body, "ABC123") {
		t.Fatalf("expected patient ABC123 in body: %q", body)
	}
}

func TestBuildMessage_UnknownTemplateErrors(t *testing.T) {
	t.Parallel()
	_, _, err := buildMessage(MessageEntry{Template: "bogus"}, "X")
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
