// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package auth validates SMART on FHIR Backend Services tokens.
package auth

// MetricsRecorder is the narrow surface the verifier and token endpoint
// need from the API metrics module. It is defined here so the auth
// package does not depend on the metrics package and to keep the
// recorder injectable from tests.
//
// reason values for RecordAuthFailure are: expired, audience, signature,
// unknown_client, revoked, replayed_jti, malformed.
type MetricsRecorder interface {
	RecordAuthFailure(reason string)
	RecordTokenIssued()
}
