# Adapter Authoring Guide

**Audience.** Anyone building a vendor EHR adapter for `fhir-ehr-subscriptions-service` — Epic, Athena, NextGen, Meditech, Oracle Health, or a hospital-specific in-tree adapter.

**What this guide does.** Walks step-by-step from "empty package" to "adapter that loads, runs, and passes the conformance harness." It does not duplicate the contract; it shows you how to satisfy it.

**Reader's prerequisites.**

1. [Adapter SPI contract](high-level-design/contracts/adapter-spi.md) — the canonical method signatures, the REQUIRED-vs-OPTIONAL split, and the versioning policy.
2. [Adapter SPI Framework LLD](low-level-design/adapter-spi-framework.md) — what the host does on your behalf (queue claim loop, transactional outbox, dead-letter routing, snapshot diffing, cache, supervisors).
3. [`adapters/default/`](../adapters/default/) — the runnable reference adapter. Read it; it is short. The shape you will write is the same shape, with vendor-specific overrides.

If you have not read those, this guide will look arbitrary. If you have, it will look obvious.

---

## What the host does for you

Before you write a line of code, internalize the line between vendor knowledge and host plumbing.

The host owns:

- The MLLP listener and the `hl7_message_queue` it writes to.
- The queue-claim loop (`SELECT FOR UPDATE SKIP LOCKED`).
- The transactional outbox: every `ResourceChange` your adapter produces is written to `resource_changes` in the same transaction that marks the source row processed.
- Dead-letter routing on translation/validation failure.
- The cancel-and-replace correlation-window state machine.
- Scan scheduling, content-hash diffing, and snapshot persistence in `adapter_state`.
- Vendor-API cursor persistence, reconnect with backoff, idempotency.
- The hydration LRU cache, request coalescing, and per-fetch hard timeout.
- HTTP client construction (auth, TLS, retries, user-agent).
- The `AdapterStateStore` (Postgres-backed KV scoped to your `adapter.id`).
- Metrics and log labels (`adapter_id`, `adapter_vendor` are pre-applied).
- The `/readyz` health surface, lifecycle ordering, graceful shutdown.

You own:

- One `EhrAdapter` subclass per vendor adapter.
- One subclass per sub-component you declare (`Hl7MessageProcessor`, `FhirScanRunner`, `VendorAPIClient`, `HydrationService`).
- Vendor-specific lex/classify/translate/normalize logic.
- A JSON Schema for your adapter's configuration block.
- Optionally, a folder of contributed `SubscriptionTopic` resources.
- The conformance harness run for your adapter (see [adapter-conformance-checklist.md](adapter-conformance-checklist.md)).

If you find yourself wanting to reach for `pgxpool`, an environment variable, or `os.Getenv`, stop. The framework gives you what you need through `AdapterContext`. Going around the sandbox is off-spec ([contract §Sandboxing](high-level-design/contracts/adapter-spi.md#sandboxing)).

---

## Step 1 — Scaffold the package

Adapters live under `adapters/<id>/`. The id matches `manifest.ID` and is the value operators set in `adapter.id` config.

```
adapters/
  default/
    default.go
    default_test.go
  acme-ehr/                      # ← your new package
    adapter.go                   # EhrAdapter subclass + manifest
    hl7.go                       # Hl7MessageProcessor subclass (if HL7Processor=true)
    scan.go                      # FhirScanRunner subclass (if FhirScanRunner=true)
    vendor.go                    # VendorAPIClient subclass (if VendorAPIClient=true)
    hydration.go                 # HydrationService subclass (if HydrationService=true)
    config_schema.json           # JSON Schema for adapter.config
    topics/                      # optional: contributed SubscriptionTopic JSON
      acme-encounter-start.json
    adapter_test.go              # unit tests
```

Naming rules:

- The id must match `[a-z0-9-]+`, no leading/trailing dash. The host validates this — see `validateAdapterID` in [`internal/adapter/spi/types.go`](../internal/adapter/spi/types.go).
- The Go package name is conventionally `<id>adapter` (e.g., `acmeadapter`). The reference adapter uses `defaultadapter`.
- One package per adapter. Do not split sub-components into separate Go packages — the host registers and constructs them through a single `EhrAdapter` factory.

Skeleton — paste this and prune what your adapter does not implement:

```go
// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

// Package acmeadapter is the Acme EHR adapter.
package acmeadapter

import (
    "context"

    "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/registry"
    "github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/adapter/spi"
)

type Adapter struct {
    spi.BaseEhrAdapter
}

func New() *Adapter { return &Adapter{} }

func NewRegistered() *registry.Registry {
    r := registry.New()
    if err := r.Register("acme-ehr", func() spi.EhrAdapter { return New() }); err != nil {
        panic(err)
    }
    return r
}

func (a *Adapter) Manifest() spi.AdapterManifest {
    return spi.AdapterManifest{
        ID:          "acme-ehr",
        Vendor:      "Acme Health Systems",
        Description: "Acme EHR — HL7 v2 ADT/ORM/ORU; FHIR R4 read API; Acme change-feed.",
        SupportedEhrVersions: spi.VersionSpec(">=2024.1"),
        Capabilities: spi.Capabilities{
            HL7Processor:     true,
            FhirScanRunner:   true,
            VendorAPIClient:  true,
            HydrationService: true,
        },
        ConfigSchema: configSchemaJSON, // //go:embed config_schema.json
        SpiVersion:   spi.HostSPIVersion,
    }
}

func (a *Adapter) BuildHl7Processor(ctx spi.AdapterContext) spi.Hl7MessageProcessor   { return newHl7(ctx) }
func (a *Adapter) BuildFhirScanRunner(ctx spi.AdapterContext) spi.FhirScanRunner     { return newScan(ctx) }
func (a *Adapter) BuildVendorAPIClient(ctx spi.AdapterContext) spi.VendorAPIClient   { return newVendor(ctx) }
func (a *Adapter) BuildHydrationService(ctx spi.AdapterContext) spi.HydrationService { return newHydration(ctx) }

// OnStart / OnShutdown inherited from BaseEhrAdapter (no-op).
// Override only if you need vendor-SDK init or shared connection setup.
```

Compare: [`adapters/default/default.go`](../adapters/default/default.go) is the same shape with everything dialed back to no-ops. Read it once before you continue — it is the simplest legal adapter.

---

## Step 2 — Declare the manifest

The manifest is the host's first point of contact. The host validates it before building anything.

### Required fields

| Field | Rule |
|---|---|
| `ID` | Lowercase `[a-z0-9-]+`, no leading/trailing dash. Must equal the registry key the factory was registered under. |
| `Vendor` | Non-empty human-readable vendor name. |
| `SpiVersion` | Set to `spi.HostSPIVersion`. The host accepts adapters whose major matches the host's; minor must be ≤ host's. |
| `Capabilities` | Each `true` field must correspond to a non-nil return from the matching `Build*`. Each `false` field must correspond to a `nil` return. Mismatch = fatal startup error. |
| `SupportedEhrVersions` | Grammar: exact `"X.Y"`, lower bound `">=X.Y"`, or `"*"` any. Operator config can pin a stricter version with `adapter.version_pin`. |
| `ConfigSchema` | A valid JSON Schema (compiled with `santhosh-tekuri/jsonschema/v5`). Empty bytes = "no validation." See `validateConfigSchema` in [`registry.go`](../internal/adapter/registry/registry.go). |

### Optional fields

| Field | Use |
|---|---|
| `Description` | Free text; surfaced in operator-facing tooling. |
| `ContributedTopics` | One or more `SubscriptionTopic` JSON resources merged into the catalog at startup. Canonical URLs must be unique within your manifest (the host enforces). See [topics catalog](low-level-design/topics.md). |

### Embed the config schema, do not inline it

```go
import _ "embed"

//go:embed config_schema.json
var configSchemaJSON []byte
```

Reasons:

- Easier to evolve the schema independently from Go code.
- Schemas typically include `$schema`, `$id`, descriptions — that is much harder to maintain inline.
- The host validates the schema compiles at startup; a malformed file fails fast.

### Capability declaration

The capability bool is the truth. The build-method return is the proof. The host cross-checks both.

- An HL7-only EHR (legacy interface, no FHIR API) declares `{HL7Processor:true, FhirScanRunner:false, VendorAPIClient:false, HydrationService:true}`. You almost always still want `HydrationService:true` because full-resource subscriptions require hydration; without it, the engine will reject any subscription whose payload type asks for full-resource.
- A FHIR-and-vendor-feed EHR declares `{HL7Processor:false, FhirScanRunner:true, VendorAPIClient:true, HydrationService:true}`.
- A capability declared `false` means the matching `Build*` MUST return `nil`. The host bails at startup if you declare `false` and return non-nil.

---

## Step 3 — Implement Hl7MessageProcessor

Implement only if `Capabilities.HL7Processor = true`.

```go
type hl7Processor struct {
    spi.BaseHl7MessageProcessor
}

func newHl7(ctx spi.AdapterContext) *hl7Processor { return &hl7Processor{} }
```

### REQUIRED overrides

`Lex(raw []byte) (ParsedHL7Message, error)` — Tokenize the raw bytes. The default reference adapter copies and returns; vendor adapters typically split into segments and walk Z-segments. Return a `ParsedHL7Message` whose `Segments any` carries your concrete typed tree (the SPI keeps it opaque; downstream `Classify` / `MapToFHIR` cast back).

`Classify(parsed ParsedHL7Message) (Classification, error)` — Decide:
- **Kind**: `ChangeCreate`, `ChangeUpdate`, or `ChangeDelete`.
- **CorrelationKey**: a vendor-stable identifier the framework uses to pair cancel-and-replace messages within `CorrelationHoldWindow`. Empty string = "no pairing." Typical sources: order number, encounter id, the EHR's session id. The key must be stable across the cancel and the replacement.

`MapToFHIR(parsed ParsedHL7Message, c Classification) (FhirResource, error)` — Produce the post-translation FHIR resource. Body must be canonical JSON. Set `ResourceType` and `ID` so downstream routing does not have to re-parse. If your vendor extension carries data the R5 base profile cannot represent, use a vendor profile and emit it; do not lose data silently.

### OPTIONAL overrides (defaults documented in [`interfaces.go`](../internal/adapter/spi/interfaces.go))

| Method | Default | Override when |
|---|---|---|
| `Validate(FhirResource) error` | permissive (no-op) | You want a stricter profile validator (FHIR Validator, vendor profile). |
| `CorrelationHoldWindow() time.Duration` | 30s | The vendor's cancel-and-replace pattern uses a different window (some vendors send replacements within seconds, others within minutes). |
| `OnUnpairedCancellation(...)` | emit plain `delete` | The vendor reuses cancellations as soft-deletes — see [ADR 0005](high-level-design/decisions/0005-cancel-and-replace-in-adapter.md). |
| `OnUnpairedReplacement(...)` | emit plain `create` | Same — vendor-specific semantics. |

### What you must NOT do

- Do **not** open a database connection. The framework writes to `resource_changes` for you, transactionally with the source row mark-processed.
- Do **not** call `ResourceChangeSink.Write` directly from your processor. Return a `ResourceChange` (or a `FhirResource` plus `Classification` from `MapToFHIR`) and let the supervisor drive the sink.
- Do **not** retry inside `Lex` / `Classify` / `MapToFHIR`. Return the error. The supervisor owns retry/backoff and dead-letter routing.

### Idempotency

Every `ResourceChange` carries a `CorrelationID uuid.UUID`. The framework provides it. Set it when you emit unpaired cancellations/replacements (the base implementations already do this). Do not invent your own.

---

## Step 4 — Implement FhirScanRunner

Implement only if `Capabilities.FhirScanRunner = true`.

```go
type scanRunner struct {
    spi.BaseFhirScanRunner
}
```

### REQUIRED overrides

`ScanPlan() []ScanTarget` — Return the list of `(ResourceType, Cadence, QueryParams)` you scan. One entry per (resource type, query) tuple. Empty plan = "no scans configured" — the framework runs zero scans.

`RunScan(ctx, target) (ScanIterator, error)` — Execute one scan target. Return a `ScanIterator` whose `Next` yields one `FhirResource` per call until `ok=false`. Implement vendor-specific paging, profile quirks, search-parameter behavior, and auth here.

### OPTIONAL overrides

| Method | Default | Override when |
|---|---|---|
| `ContentHash(FhirResource) string` | hex SHA-256 of `Body` | You need [RFC 8785 JCS](https://datatracker.ietf.org/doc/html/rfc8785) canonicalization, or you want to strip vendor-volatile fields (`meta.lastUpdated`, vendor extensions) before hashing — see [ADR 0010](high-level-design/decisions/0010-implementation-defaults.md). |
| `Normalize(FhirResource) FhirResource` | identity | Vendor profiles deviate from R5 and you want to map back to base R5 before downstream stages see the resource. |

### How diffing works (so you do not reinvent it)

The framework calls `RunScan` on cadence. For each yielded resource:

1. The framework computes `ContentHash(resource)`.
2. It compares against the snapshot stored under `snapshot:<ResourceType>:<ID>` in `adapter_state` (your `AdapterStateStore`).
3. New resource (no snapshot): emits `create`. Hash differs: emits `update` with both `previous_resource` and the new one. Hash matches: skips.
4. After the iterator returns `ok=false`, the framework emits `delete` for any snapshot whose key did not appear in this scan.

You do not write to `adapter_state` for snapshots — the framework owns those keys. You may write your own keys for your own state (e.g., paging cursors that survive process restart), but use a different prefix than `snapshot:`.

---

## Step 5 — Implement VendorAPIClient

Implement only if `Capabilities.VendorAPIClient = true`.

```go
type vendorClient struct{}
func newVendor(ctx spi.AdapterContext) *vendorClient { return &vendorClient{} }
```

There is no `Base` for `VendorAPIClient` — both methods are required.

### `Consume(ctx, sink EventSink, cursor []byte) error`

Long-running consumer. The framework calls this in a supervised loop. You stream events to the provided `sink` via `sink.Push(ctx, VendorRecord{Cursor, Payload, EventCode})`.

Rules:

- Honor `ctx.Done()`. Return `ctx.Err()` on cancellation.
- The `cursor` argument is the cursor the framework persisted last time. If `nil`, you are starting fresh.
- Set `VendorRecord.Cursor` on every push so the framework can advance the persisted cursor only after the resulting `resource_changes` row commits. **If you don't set it, the cursor never advances.**
- Set `VendorRecord.EventCode` if your vendor stamps an event-type code. The engine consumes it for `SubscriptionTopic.eventTrigger` matching.
- Do **not** call your own retry loop on connection drops. Return the error; the supervisor owns reconnect with exponential backoff.

### `Translate(record VendorRecord) (ResourceChange, error)`

Pure function. Take one record, produce one `ResourceChange`. The framework calls this for each record `Push`'d onto the sink. Returned errors route to dead-letter, not to retry.

---

## Step 6 — Implement HydrationService

Implement only if `Capabilities.HydrationService = true`. Most adapters need this — full-resource subscriptions cannot be served without it.

```go
type hydration struct {
    spi.BaseHydrationService
}
```

### REQUIRED override

`Fetch(ctx, ref FhirReference) (FhirResource, error)` — Fetch one resource by reference from the vendor. Return the resource body in canonical JSON.

### OPTIONAL override

`CacheTTL() time.Duration` — default 60s. The framework caches your responses per-replica with this TTL; concurrent calls for the same reference deduplicate to one fetch. Override only if you have a strong reason (e.g., vendor recommends 10s for ICU resources because of clinical staleness concerns).

### What the framework caches and coalesces

The cache key is `(ResourceType, ID)`. A second concurrent caller for the same key blocks on the in-flight first caller and gets the cached result. The TTL is a soft TTL — entries past TTL are evicted lazily.

Hard timeout per fetch is a framework concern; your `Fetch` does not need to enforce one, but it should respect `ctx.Done()`.

---

## Step 7 — Configuration schema

`config_schema.json` is a JSON Schema (Draft 7+) that validates `adapter.config` from the host config file before your `Build*` methods see it. Keep it strict.

Example for an adapter with FHIR base + auth:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://fhir-ehr-subscriptions-service/schemas/adapter/acme-ehr/config",
  "type": "object",
  "additionalProperties": false,
  "required": ["fhir_base_url", "auth"],
  "properties": {
    "fhir_base_url": {
      "type": "string",
      "format": "uri",
      "description": "Acme FHIR R4 base URL."
    },
    "auth": {
      "type": "object",
      "additionalProperties": false,
      "required": ["kind", "client_id"],
      "properties": {
        "kind": { "const": "smart-backend-services" },
        "client_id": { "type": "string", "minLength": 1 },
        "private_key_file": { "type": "string", "minLength": 1 },
        "token_url": { "type": "string", "format": "uri" }
      }
    }
  }
}
```

Receive it in your sub-component constructor:

```go
type acmeConfig struct {
    FHIRBaseURL string         `json:"fhir_base_url"`
    Auth        acmeAuthConfig `json:"auth"`
}

func newScan(ctx spi.AdapterContext) *scanRunner {
    var cfg acmeConfig
    if err := json.Unmarshal(ctx.Config, &cfg); err != nil {
        panic("acmeadapter: config did not unmarshal — schema validation should have caught this")
    }
    return &scanRunner{cfg: cfg, http: ctx.HTTP}
}
```

The host has already validated `ctx.Config` against your schema; if `Unmarshal` fails here, the schema or the Go type is wrong — that is a programmer error.

---

## Step 8 — Register and wire

Adapters live in the bundled-adapter registry. Register from your factory:

```go
func NewRegistered() *registry.Registry {
    r := registry.New()
    if err := r.Register("acme-ehr", func() spi.EhrAdapter { return New() }); err != nil {
        panic(err)
    }
    return r
}
```

Then wire your adapter into the host's adapter assembly. The host (`cmd/fhir-subs/`) constructs the registry by registering every bundled adapter. The runtime selects one via `adapter.id` config; it does not run two simultaneously.

For startup-time selection, `cfg.HostSpiVer = spi.HostSPIVersion`. The registry runs the LLD §4 validations in order:

1. `UnknownAdapterError` — id not in registry.
2. `SpiMajorMismatchError` — manifest spi major != host major, OR adapter minor > host minor.
3. `ManifestIDMismatchError` — registry key != `manifest.ID`.
4. `VersionPinUnsatisfiableError` — operator's `adapter.version_pin` outside `manifest.SupportedEhrVersions`.
5. `manifest.Validate()` — id pattern, vendor non-empty, spi non-zero.
6. Stateful: `config_schema` compiles, `contributed_topics` URLs unique.

Each error is structured (typed) so operators get an actionable message, not a stack trace.

---

## Step 9 — Run the conformance harness

Every adapter — including `adapters/default` and yours — must pass the conformance harness from [LLD §9.2](low-level-design/adapter-spi-framework.md#92-conformance-tests-every-adapter-must-pass).

Use the [adapter-conformance-checklist.md](adapter-conformance-checklist.md) test matrix as your acceptance criteria. The mappings:

| Conformance area | What it proves | How to run |
|---|---|---|
| Manifest validation | `Validate()` passes; `Build*` returns match capability bools; `ConfigSchema` compiles. | Unit test against `registry.Load` — see `TestDefaultManifestIsRegistrable`. |
| Lifecycle | `OnStart` runs, supervisors start, `/readyz` flips ready, shutdown drains. | `e2e/orchestrator/` startup test against your `adapter.id`. |
| HL7 happy path | A synthetic HL7 message produces an expected `resource_changes` row, source row marked processed. | Inject through the MLLP listener (or directly into `hl7_message_queue`); assert via the orchestrator harness. |
| HL7 dead-letter | Malformed message routes to `dead_letters`, not `resource_changes`. Source row still marked processed. | As above with a deliberately malformed payload. |
| HL7 cancel-and-replace | Paired cancel/new produces one `update` row with both `previous_resource` and `resource`. | Two messages within `CorrelationHoldWindow` sharing `CorrelationKey`. |
| FHIR scan happy path | First scan emits `create`, modified resource emits `update`, removed resource emits `delete`. | Stub FHIR server + scan runner harness. |
| FHIR scan rate limit | Exceeding budget defers without crashing. | Lower budget; assert deferral metric. |
| Vendor API happy path | Events translate to rows; cursor persists only after row commits; reconnect resumes. | Stub change-feed harness. |
| Hydration happy path | Concurrent calls dedupe; cache hits avoid EHR call; timeout returns `TransientFailure`. | Hydration harness with controllable EHR. |
| State scoping | Adapter cannot read another adapter's state. | Negative test against `AdapterStateStore`. |

The reference unit tests for `adapters/default/` ([`default_test.go`](../adapters/default/default_test.go)) exercise the manifest, the registry round-trip, the build methods, and the inherited base behavior. Mirror them for your adapter as the **first** layer — they are fast and they catch most authoring mistakes before the full conformance harness runs.

The full harness runs in `e2e/orchestrator/`. CI runs it against the default adapter; you are responsible for adding a per-adapter stage that runs it against yours before your adapter ships.

---

## Common authoring mistakes

These come up repeatedly in adapter reviews. Save yourself the round trip.

1. **Returning a non-nil sub-component for a `false` capability.** The host refuses to start. If you do not implement vendor APIs, declare `VendorAPIClient: false` AND return `nil` from `BuildVendorAPIClient`.
2. **Mismatched manifest id and registry key.** `r.Register("acme", ...)` and `Manifest().ID = "acme-ehr"` is a `ManifestIDMismatchError` at load time. They are the same string.
3. **Reaching for `os.Getenv` or environment variables.** Configuration values that look like environment placeholders are resolved by the host before they hit your `ctx.Config`. The adapter does not read environment variables directly.
4. **Database access from inside the adapter.** Your adapter has no `*pgxpool.Pool`. The framework writes to `resource_changes` for you. Use `AdapterStateStore` for your own state.
5. **Calling `ResourceChangeSink.Write` directly from your processor.** That is the supervisor's job. Return a `ResourceChange` (or the parts of one) and let the host take it from there.
6. **Setting `CorrelationID = uuid.Nil` on a `ResourceChange`.** `Validate()` rejects it; the row routes to dead-letter. The base unpaired-pair handlers already set this correctly — emulate them.
7. **Forgetting to set `VendorRecord.Cursor` on every push.** The framework persists the cursor only when set; without it, you reprocess the same events on every reconnect.
8. **Retrying inside your sub-component on a connection drop.** The supervisor owns retry/backoff. Surface the error and exit; the supervisor reconnects.
9. **Calling `state_store.Put` with un-namespaced keys that collide with framework keys.** The framework owns `snapshot:*` and `cursor:*` for your scan and vendor sub-components. Pick a different prefix for your own state (e.g., `myadapter:auth-token`).
10. **Inlining `config_schema` into Go source.** Embed the JSON file with `//go:embed`. Schemas should be inspectable by operators without reading Go.
11. **Declaring a `version_pin`-incompatible `SupportedEhrVersions`.** Operators set `adapter.version_pin` to lock a deployment. If your manifest declares `>=2024.1` and the operator pins `>=2023.4`, the load fails with `VersionPinUnsatisfiableError`. Make sure your supported range is honest.
12. **Treating `Translate` errors as transient.** `Translate` errors route to dead-letter, not retry. If you genuinely have a transient (network, vendor 5xx) failure, that belongs in `Consume`, not in `Translate`.

---

## Iterating on your adapter

The framework lets you start small and grow:

1. Stage 1: ship an adapter with `HL7Processor: true` only. The default `Validate` is permissive; you can defer profile compliance to Stage 2.
2. Stage 2: add `HydrationService` so full-resource subscriptions work.
3. Stage 3: add `FhirScanRunner` for periodic resource discovery.
4. Stage 4: add `VendorAPIClient` if your vendor offers a real-time change feed.

Each stage adds a capability bool. The host loads only what is declared, so partial adapters are first-class.

When in doubt: the simplest legal adapter is `adapters/default`. Read it, copy it, override what differs.

---

## Where to look when something goes wrong

| Symptom | First place to look |
|---|---|
| Process refuses to start with `unknown adapter` | `cmd/fhir-subs/` registry assembly. Make sure your factory is registered. |
| `SpiMajorMismatchError` | `manifest.SpiVersion` is not `spi.HostSPIVersion`. Set it from the constant. |
| `ManifestIDMismatchError` | Registry key != `manifest.ID`. |
| Schema does not compile | Lint your `config_schema.json` separately first. The host uses `santhosh-tekuri/jsonschema/v5`. |
| Capability mismatch | `Capabilities.X = true` but `BuildX` returns nil. Or the inverse. |
| Cancel-and-replace produces two rows instead of one | `Classify().CorrelationKey` is empty or differs between the cancel and the replacement. |
| FHIR scan emits spurious `update` rows | Default `ContentHash` includes vendor-volatile fields (`meta.lastUpdated`). Override to strip them. |
| Vendor cursor never advances | `VendorRecord.Cursor` not set on `Push`. |
| `Fetch` returns slow but cache never hits | Two callers asking for slightly different `FhirReference` shapes (different `ResourceType` casing). Normalize. |

---

## Next steps

- Read the [conformance checklist](adapter-conformance-checklist.md). Run it locally against your adapter early — it catches authoring bugs cheaper than CI does.
- Read [Adapter SPI Framework LLD §3](low-level-design/adapter-spi-framework.md) for the public surface (`AdapterFramework`) the host wraps you with.
- Read the per-supervisor LLDs for the runtime detail of each sub-component:
  - [hl7-message-processor.md](low-level-design/hl7-message-processor.md)
  - [fhir-scan-runner.md](low-level-design/fhir-scan-runner.md)
  - [vendor-api-and-hydration.md](low-level-design/vendor-api-and-hydration.md)
- Read the relevant ADRs:
  - [0005 — Cancel-and-replace lives in the adapter](high-level-design/decisions/0005-cancel-and-replace-in-adapter.md)
  - [0007 — Spec-bounded scope](high-level-design/decisions/0007-spec-bounded-scope.md)
  - [0010 — Implementation defaults (canonical JSON, validator, etc.)](high-level-design/decisions/0010-implementation-defaults.md)
