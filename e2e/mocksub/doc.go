// Copyright the fhir-subscriptions-foss authors.
// SPDX-License-Identifier: Apache-2.0

// Package mocksub is the e2e harness's subscriber mock. It owns:
//
//   - An HTTPS rest-hook receiver with a journal of received notifications
//     and a control-plane HTTP API for the orchestrator.
//   - A WSS client that connects to fhir-subs' websocket channel and
//     journals every frame.
//   - A fake SMTP receiver that journals every message and exposes them
//     through the same control-plane shape as the rest-hook journal.
//
// The mock is the assert-side of every scenario: scenarios push a stimulus
// into the EHR mock, fhir-subs translates and routes it, and the
// subscriber mock catches what fhir-subs delivers. The orchestrator polls
// the subscriber mock's control plane to make assertions.
package mocksub
