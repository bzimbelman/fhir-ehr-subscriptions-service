// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package mockehr is the e2e harness's EHR mock. It owns:
//
//   - HL7 v2.x message builders for the trigger events the LLD ships
//     against (ADT, ORM new/cancel/replace, ORU, SIU, MDM).
//   - An MLLP server that frames + writes those messages on the wire.
//   - A FHIR REST mock with an in-memory store of the resource types the
//     v0 schema and the topic catalog touch.
//   - A vendor change-feed mock (HTTP + SSE).
//   - A scenario control plane HTTP API used by the orchestrator.
//
// The mock has no production use. It exists so e2e scenarios can drive a
// reproducible EHR-side stimulus into fhir-subs and assert what falls out
// the other side.
package mockehr
