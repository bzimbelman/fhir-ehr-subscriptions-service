// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package harness wires the production stages — hl7processor, matcher,
// submatcher, builder, scheduler — and the rest-hook / WSS / email
// channels together against a Postgres pool, so e2e scenarios can drive
// real ADT/ORM messages through the whole bridge and observe deliveries
// at the subscriber side.
//
// The harness is intentionally black-box from the orchestrator's point
// of view: it imports only public APIs of `internal/...` packages and
// keeps the wiring shape stable so tests can swap individual stages in
// and out (e.g., disable the scheduler to force backpressure).
package harness
