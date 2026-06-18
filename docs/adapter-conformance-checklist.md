# Adapter Conformance Checklist

**Purpose.** A concrete, line-item checklist every adapter (`adapters/default`, vendor adapters, third-party adapters) must satisfy before it ships. Pair this with the prose [adapter-authoring-guide.md](adapter-authoring-guide.md) and the test plan in [adapter-spi-framework.md §9.2](low-level-design/adapter-spi-framework.md#92-conformance-tests-every-adapter-must-pass).

**How to use.** Treat each row below as an acceptance criterion. Tick boxes as you write the corresponding test. The `Reference test` column points at the matching test in `adapters/default/` (when it exists today) or names the harness you should add.

A row marked **N/A** is one that does not apply because the adapter declared the corresponding capability `false`. Capability-gated rows are explicitly marked.

---

## Section A — Manifest & registration

The adapter loads cleanly through the bundled-adapter registry.

| # | Criterion | Reference test |
|---|---|---|
| A1 | `Manifest().Validate()` returns `nil`. | `TestDefaultManifestShape` in [`adapters/default/default_test.go`](../adapters/default/default_test.go). |
| A2 | `Manifest().ID` matches the registry key the factory was registered under. | `registry.Load` round-trip; see `TestDefaultManifestIsRegistrable`. |
| A3 | `Manifest().SpiVersion == spi.HostSPIVersion`. | `TestDefaultManifestShape`. |
| A4 | `Manifest().Vendor` is non-empty. | `TestDefaultManifestShape`. |
| A5 | `Manifest().ID` matches `[a-z0-9-]+`, no leading/trailing dash. | Enforced by `validateAdapterID`; assert in your test. |
| A6 | Every `Capabilities.X = true` corresponds to a non-nil `BuildX(ctx)`. | `TestDefaultBuildersHonorCapabilities`. |
| A7 | Every `Capabilities.X = false` corresponds to `BuildX(ctx) == nil`. | `TestDefaultBuildersHonorCapabilities`. |
| A8 | `ConfigSchema` is empty OR compiles as a JSON Schema (Draft 7+). | `validateConfigSchema` in `registry.go`; assert by calling `registry.Load`. |
| A9 | Each `ContributedTopics[i]` parses as JSON; canonical URLs are unique within the manifest. | `validateContributedTopicsUnique` in `registry.go`. |
| A10 | `SupportedEhrVersions` is `"*"`, an exact `"X.Y"`, or a `">=X.Y"` lower bound. | Assert via `VersionSpec.Satisfies` against probe pins. |
| A11 | An operator `version_pin` that the manifest does not satisfy fails registry load with `VersionPinUnsatisfiableError`. | Negative test in your conformance suite. |
| A12 | A registry-key/manifest-id mismatch fails with `ManifestIDMismatchError`. | Negative test in your conformance suite. |
| A13 | A spi-major mismatch fails with `SpiMajorMismatchError`. | Negative test in your conformance suite. |

---

## Section B — Lifecycle

The adapter starts, runs, and shuts down cleanly under the supervisor coordinator.

| # | Criterion | Reference test |
|---|---|---|
| B1 | `OnStart(ctx, actx)` returns `nil` for a valid `AdapterContext`. | `TestDefaultLifecycleHooks`. |
| B2 | `OnShutdown(ctx, actx)` returns `nil`. | `TestDefaultLifecycleHooks`. |
| B3 | All declared sub-components start without panic in their constructors. | Build harness: call `BuildHl7Processor`, `BuildFhirScanRunner`, etc. with a stub `AdapterContext`. |
| B4 | `/readyz` flips to ready after every declared supervisor reports running. | `e2e/orchestrator/` startup test scoped to your `adapter.id`. |
| B5 | Graceful shutdown drains in-flight work within the configured grace period. | `e2e/orchestrator/` graceful-shutdown test scoped to your `adapter.id`. |
| B6 | A supervisor failure surfaces as `fhir_subs_adapter_supervisor_state{component=...}=failed`. | Forced-failure harness in conformance. |

---

## Section C — HL7 v2 (if `Capabilities.HL7Processor = true`)

`N/A` if the adapter declared `HL7Processor = false`.

| # | Criterion | Reference test |
|---|---|---|
| C1 | `Lex(raw)` preserves `parsed.Raw` byte-for-byte. (Adapters that need to drop trailing CR may special-case but must document.) | `TestDefaultHl7ProcessorPassesThrough`. |
| C2 | `Classify(parsed).Kind` is one of `ChangeCreate / ChangeUpdate / ChangeDelete`. | `TestDefaultHl7ProcessorPassesThrough`. |
| C3 | `MapToFHIR` returns a `FhirResource` with non-empty `ResourceType` and non-empty `Body`. | `TestDefaultHl7ProcessorPassesThrough`. |
| C4 | `Validate(resource)` returns `nil` for a happy-path resource. | `TestDefaultHl7ProcessorPassesThrough`. |
| C5 | `CorrelationHoldWindow()` returns the documented value (default 30s, override allowed). | `TestDefaultHl7ProcessorPassesThrough`. |
| C6 | `OnUnpairedCancellation` emits `ChangeDelete`. | `TestDefaultHl7ProcessorPassesThrough`. |
| C7 | `OnUnpairedReplacement` emits `ChangeCreate`. | `TestDefaultHl7ProcessorPassesThrough`. |
| C8 | A synthetic happy-path HL7 message produces exactly one `resource_changes` row matching the expected shape. | Conformance harness against `hl7_message_queue`. |
| C9 | A malformed HL7 message routes to `dead_letters`. The source row is still marked processed; no `resource_changes` row is written. | Conformance harness with deliberately malformed payload. |
| C10 | A paired cancel-and-replace within `CorrelationHoldWindow` produces one `update` row with both `previous_resource` and `resource` populated. | Conformance harness with two messages sharing `Classify.CorrelationKey`. |
| C11 | An unpaired cancellation honors the configured `OnUnpairedCancellation`. | Conformance harness; emit cancellation, advance time past hold window. |
| C12 | `Validate()` rejection routes to `dead_letters` (not retried). | Conformance harness with a resource that fails validation. |
| C13 | A returned `ResourceChange` with `CorrelationID == uuid.Nil` is rejected by `Validate()` and routed to dead-letter. | `ResourceChange.Validate` test. |

---

## Section D — FHIR scan (if `Capabilities.FhirScanRunner = true`)

`N/A` if the adapter declared `FhirScanRunner = false`.

| # | Criterion | Reference test |
|---|---|---|
| D1 | `ScanPlan()` returns a non-nil slice. Empty slice is allowed and means "no scans"; the framework runs none. | `TestDefaultFhirScanRunnerEmptyPlan`. |
| D2 | `RunScan(ctx, target)` returns a `ScanIterator` (or `(nil, error)`); the iterator's `Next` returns `(_, false, nil)` when exhausted. | Add for non-empty plans. |
| D3 | `ContentHash(resource)` is deterministic for byte-equal inputs. | Hash twice; compare. |
| D4 | `Normalize(resource)` is identity by default; if overridden, the result preserves the resource's identity (`ResourceType` + `ID`). | `TestDefaultFhirScanRunnerEmptyPlan` shows the identity case. |
| D5 | First scan of a previously-unseen resource emits `ChangeCreate`. | Conformance harness with stub FHIR server. |
| D6 | A subsequent scan with a modified body (different `ContentHash`) emits one `ChangeUpdate` with `previous_resource` populated. | Same harness, mutate body. |
| D7 | A scan in which a previously-seen resource is absent emits `ChangeDelete`. | Same harness, remove resource. |
| D8 | A scan in which the resource is byte-equal to the snapshot emits zero rows. | Same harness, repeat. |
| D9 | Exceeding the configured rate-limit budget defers the scan without crashing the supervisor. | Conformance harness with low budget. |
| D10 | The adapter does not write to `snapshot:*` keys directly (those are framework-owned). | Code review. |

---

## Section E — Vendor API client (if `Capabilities.VendorAPIClient = true`)

`N/A` if the adapter declared `VendorAPIClient = false`.

| # | Criterion | Reference test |
|---|---|---|
| E1 | `Consume(ctx, sink, cursor)` honors `ctx.Done()` and returns `ctx.Err()` on cancellation. | Forced-cancel harness. |
| E2 | Every `sink.Push(record)` carries a non-nil `record.Cursor`. | Code review + harness assertion. |
| E3 | The framework persists the cursor only after the resulting `resource_changes` row commits. | Conformance harness: kill before commit, restart, verify cursor unchanged. |
| E4 | A forced reconnect resumes consumption from the persisted cursor. | Conformance harness with restart. |
| E5 | `Translate(record)` is pure (deterministic for the same input). | Translate twice; assert byte-equal `ResourceChange`. |
| E6 | `Translate` errors route to dead-letter, not retry. | Conformance harness with a record that triggers a `Translate` error. |
| E7 | `Consume` does not implement its own retry/backoff on connection failure. | Code review. |
| E8 | `record.EventCode` is set when the vendor stamps an event-type code (so `SubscriptionTopic.eventTrigger` matches). | Sample event with known event code; assert preservation in `resource_changes.event_code`. |

---

## Section F — Hydration (if `Capabilities.HydrationService = true`)

`N/A` if the adapter declared `HydrationService = false`.

| # | Criterion | Reference test |
|---|---|---|
| F1 | `Fetch(ctx, ref)` returns the resource body in canonical JSON. | Direct test. |
| F2 | `Fetch` honors `ctx.Done()` and returns `ctx.Err()` on cancellation. | Forced-cancel harness. |
| F3 | `CacheTTL()` returns the documented value (default 60s, override allowed). | `TestDefaultHydrationCacheTTL`. |
| F4 | Two concurrent calls for the same `FhirReference` deduplicate to one underlying EHR fetch. | Hydration harness with controllable EHR. |
| F5 | A second sequential call within `CacheTTL` returns the cached resource without calling `Fetch`. | Same harness. |
| F6 | A per-fetch hard timeout surfaces as `TransientFailure` to the calling channel. | Same harness with a slow EHR. |

---

## Section G — Sandbox & state isolation

These apply to every adapter regardless of capability declarations.

| # | Criterion | Reference test |
|---|---|---|
| G1 | The adapter does not import `database/sql`, `pgx`, or `pgxpool`. | `go vet` / lint rule; code review. |
| G2 | The adapter does not call `os.Getenv` or read `os.Environ`. | `grep` audit. |
| G3 | The adapter does not call `os.Open` on paths outside its own embedded files. | Code review. |
| G4 | Two adapters with different ids cannot read each other's `AdapterStateStore` keys. | Conformance harness with two registered adapters. |
| G5 | (Future, when egress allow-listing lands.) The adapter's HTTP client cannot reach hosts outside its declared `manifest.allowed_egress`. | Marked open in [LLD §12](low-level-design/adapter-spi-framework.md#12-open-questions); not enforced today. |

---

## Section H — Test hygiene

| # | Criterion | Reference test |
|---|---|---|
| H1 | Unit tests cover every override the adapter declares (`TestXManifestShape`, `TestXBuildersHonorCapabilities`, per-sub-component tests). | Mirror `adapters/default/default_test.go`. |
| H2 | All unit tests pass with `-race`. | CI. |
| H3 | All unit tests pass with `gofmt -d ./adapters/<id>/...` clean. | CI. |
| H4 | A registry round-trip test exists (`registry.Load(LoadConfig{HostSpiVer: spi.HostSPIVersion, AdapterID: "<your id>"})`). | `TestDefaultManifestIsRegistrable`. |
| H5 | The conformance harness from [LLD §9.2](low-level-design/adapter-spi-framework.md#92-conformance-tests-every-adapter-must-pass) runs against this adapter in CI before the adapter ships. | New stage in `e2e/orchestrator/`. |

---

## Sign-off

An adapter is conformant when:

1. Every Section A and Section B row is checked.
2. Every Section C/D/E/F row corresponding to a declared capability is checked.
3. Every Section G and Section H row is checked.

The owning team signs off in the adapter's PR description: paste this checklist, mark each row, and link the test that proves it.
