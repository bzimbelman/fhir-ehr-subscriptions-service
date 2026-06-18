// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package orchestrator drives end-to-end scenarios against fhir-subs.
//
// It owns:
//
//   - A TestMain that starts a Postgres container via
//     testcontainers-go, applies migrations/0001_init.sql, and stands up
//     the mockehr + mocksub HTTP handlers.
//   - A small set of helpers (RegisterSubscriber, WaitForNotification,
//     AssertResourceChanges) that scenario tests use to express their
//     intent without hand-coding postgres/HTTP plumbing.
//   - One *_test.go file per scenario from the LLD. The two v1
//     scenarios (`smoke_listener_ack` and `smoke_persist`) run end-to-end
//     against a stub MLLP listener mocked locally; the rest are
//     committed with t.Skip() until their owning component lands.
//
// Tests are gated with the `e2e` build tag — `go test -tags e2e ./e2e/...`
// — so they don't run in the unit-test suite by default.
package orchestrator
