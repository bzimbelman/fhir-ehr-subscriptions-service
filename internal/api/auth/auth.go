// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package auth validates SMART on FHIR Backend Services tokens.
package auth

// MetricsRecorder is the narrow surface the verifier and token endpoint
// need from the API metrics module. It is defined here so the auth
// package does not depend on the metrics package and to keep the
// recorder injectable from tests.
//
// reason values for RecordAuthFailure include: expired, audience,
// signature, unknown_client, revoked, replayed_jti, malformed,
// method_not_allowed, body_too_large, parse_form_failed,
// unsupported_grant_type, rate_limited, server_error, and the granular
// assertion-shape rejections (assertion_type_mismatch, missing_*,
// iss_mismatch, sub_mismatch, exp_invalid, client_lookup_failed). Every
// reason is mapped through diagnosticForReason so the on-the-wire
// diagnostic stays caller-agnostic (OP #221).
type MetricsRecorder interface {
	RecordAuthFailure(reason string)
	RecordTokenIssued()
	// RecordJWKSSingleflightCollision is called once per concurrent
	// JWKS fetch that joined an in-flight singleflight group instead
	// of issuing its own HTTP request (OP #202). When 1000 concurrent
	// first-time requests for the same URL arrive, exactly 1 fetch is
	// issued and 999 collisions are recorded.
	RecordJWKSSingleflightCollision()
}
