// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package fhirerror builds FHIR OperationOutcome bodies and writes them
// to an http.ResponseWriter. Every error response in the Subscriptions
// API is an OperationOutcome per the spec.
package fhirerror

import (
	"encoding/json"
	"net/http"
)

// Severity values used on OperationOutcome.issue[*].severity.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// IssueType values used on OperationOutcome.issue[*].code per the
// IssueType ValueSet (https://hl7.org/fhir/R5/valueset-issue-type.html).
const (
	CodeInvalid       = "invalid"
	CodeStructure     = "structure"
	CodeValue         = "value"
	CodeNotFound      = "not-found"
	CodeForbidden     = "forbidden"
	CodeSecurity      = "security"
	CodeLogin         = "login"
	CodeConflict      = "conflict"
	CodeNotSupported  = "not-supported"
	CodeBusinessRule  = "business-rule"
	CodeProcessing    = "processing"
	CodeException     = "exception"
	CodeTransient     = "transient"
	CodeThrottled     = "throttled"
)

// Issue is one entry in OperationOutcome.issue.
type Issue struct {
	Severity    string `json:"severity"`
	Code        string `json:"code"`
	Diagnostics string `json:"diagnostics,omitempty"`
}

// OperationOutcome is the FHIR error envelope.
type OperationOutcome struct {
	ResourceType string  `json:"resourceType"`
	Issue        []Issue `json:"issue"`
}

// New constructs an OperationOutcome with a single issue.
func New(severity, code, diagnostics string) *OperationOutcome {
	return &OperationOutcome{
		ResourceType: "OperationOutcome",
		Issue: []Issue{{
			Severity:    severity,
			Code:        code,
			Diagnostics: diagnostics,
		}},
	}
}

// Write serializes oo to w with status. Sets the FHIR JSON content type.
func Write(w http.ResponseWriter, status int, oo *OperationOutcome) {
	w.Header().Set("Content-Type", "application/fhir+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oo)
}

// WriteError is a convenience that builds and writes a single-issue
// OperationOutcome with severity=error.
func WriteError(w http.ResponseWriter, status int, code, diagnostics string) {
	Write(w, status, New(SeverityError, code, diagnostics))
}
