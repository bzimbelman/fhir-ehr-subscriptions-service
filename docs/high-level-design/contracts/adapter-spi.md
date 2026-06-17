# Contract: Adapter SPI

**Purpose.** The interface a vendor EHR adapter implements. The most important interface in the system because it determines whether the project is genuinely extensible or just configurable. Stability is a long-term goal; breaking changes will be versioned.

**Reader's prerequisites.** Read [../domains/ehr-adapter.md](../domains/ehr-adapter.md) (the domain doc) and `../../architecture.md` (section "Adapter SPI" — canonical signatures with REQUIRED vs OPTIONAL overrides for every base class). This contract reproduces the architecture's signatures and adds per-call semantics, framework guarantees, and the versioning policy.

## Design principles

The architecture lists the principles. The HLD-relevant points:

- **Capability-driven, not assumption-driven.** An adapter declares which sub-components it provides via `manifest()`. The host never assumes an EHR has FHIR or HL7 — an HL7-only adapter is just as valid as a FHIR-only one.
- **The adapter speaks FHIR resources to the core.** Translation from HL7 v2 / vendor formats into FHIR is the adapter's job. The core's domain language is FHIR.
- **Adapters are stateless except for scan/cursor state.** Per-EHR state (resource snapshots for diffing, change-feed cursors, sequence numbers) is persisted via the host-injected `AdapterStateStore`.
- **Adapters are sandboxable.** No direct access to the database, no direct access to the network beyond what they declare. The host injects an HTTP client, a state store, a logger, a metrics emitter.
- **Base classes own cross-cutting concerns.** The vendor subclass overrides only marked methods. Everything else (DB I/O, queue claiming, transactional outbox writes, dead-letter routing, metric emission, lifecycle, retry/backoff, idempotency) is the framework's job.

## The four base classes

The SPI is **not** a single flat trait. It is a small framework: each of the four sub-components ships a concrete base class with working defaults. The reference implementation (`adapters/default`) uses the bases as-is; vendor adapters override only what differs.

### `EhrAdapter` (top-level)

Top-level lifecycle and registration. Owns no I/O. Rarely overridden beyond the manifest.

```
abstract class EhrAdapter {
    // === REQUIRED override ===
    abstract fn manifest() -> AdapterManifest

    // === REQUIRED override ===
    // Construct each declared sub-component. Implementors return their own
    // subclass of the corresponding base class.
    abstract fn build_hl7_processor(ctx)      -> Hl7MessageProcessor?
    abstract fn build_fhir_scan_runner(ctx)   -> FhirScanRunner?
    abstract fn build_vendor_api_client(ctx)  -> VendorApiClient?
    abstract fn build_hydration_service(ctx)  -> HydrationService?
    //   Return null for any sub-component the EHR does not support.

    // === OPTIONAL overrides ===
    // Default: no-op. Override for adapter-wide setup/teardown
    // (e.g., shared connection pool, vendor SDK initialization).
    fn on_start(ctx)     { /* default: no-op */ }
    fn on_shutdown(ctx)  { /* default: no-op */ }
}
```

The host calls `manifest()`, validates the declared capabilities against deployment configuration, instantiates each sub-component the adapter built, calls `on_start`, and runs the lifecycle.

### `Hl7MessageProcessor` — Stage 1 source for HL7 v2

The base class owns the queue loop. The vendor subclass plugs into the four translation steps and the cancel-and-replace correlation.

```
abstract class Hl7MessageProcessor {
    // === Provided by the framework — DO NOT override: ===
    //   - claim loop over hl7_message_queue using FOR UPDATE SKIP LOCKED
    //   - in-memory wakeup wiring from the MLLP listener
    //   - mark-processed + insert resource_changes in one transaction
    //   - dead-letter routing on translation/validation failure
    //   - metrics: messages_processed, processing_duration_ms,
    //     dead_lettered_total per error class
    //   - per-message correlation-id propagation
    //   - cancel-and-replace correlation-window state machine
    //     (Postgres-backed pending table)

    // === REQUIRED overrides ===
    // Step 7: tokenize the raw HL7 message into a typed tree.
    // Most adapters extend rather than replace.
    fn lex(raw_bytes) -> ParsedHl7Message

    // Step 8: derive (change_kind, vendor_correlation_key) from the message.
    // The correlation_key is what the framework uses to pair cancel-and-replace
    // pairs within the configured hold window.
    fn classify(parsed) -> Classification { kind, correlation_key }

    // Step 9: produce the FHIR resource(s) for one message.
    fn map_to_fhir(parsed, classification) -> FhirResource

    // === OPTIONAL overrides ===
    // Step 10: validate the produced resource against the project's profile set.
    // Default: validate against base R5 resource profile.
    fn validate(resource) -> ValidationResult { /* default: base profile */ }

    // Hold window for cancel-and-replace pairing. Default: 30s.
    fn correlation_hold_window() -> Duration { /* default: 30s */ }

    // What to do when the hold window expires without a partner.
    fn on_unpaired_cancellation(resource) -> ResourceChange { /* default: emit plain delete */ }
    fn on_unpaired_replacement(resource)  -> ResourceChange { /* default: emit plain create */ }
}
```

### `FhirScanRunner` — Stage 1 source for periodic FHIR scans

```
abstract class FhirScanRunner {
    // === Provided by the framework: ===
    //   - scheduling (per-resource cadence)
    //   - snapshot persistence in adapter_state
    //   - content-hash diffing
    //   - resource_changes row writing for create/update/delete deltas
    //   - rate-limit budget enforcement against EHR
    //   - retry/backoff on transient EHR errors
    //   - metrics: scans_run, resources_scanned, deltas_emitted

    // === REQUIRED overrides ===
    // The set of FHIR resource types the adapter scans, with cadences.
    fn scan_plan() -> [ScanTarget { resource_type, cadence, query_params }]

    // Execute one scan target's query against the EHR's FHIR API.
    // Implementors handle vendor-specific paging, profile quirks,
    // search-parameter behavior, and auth.
    abstract fn run_scan(target, http) -> Iterator<FhirResource>

    // === OPTIONAL overrides ===
    // Hash function for diffing. Default: canonical JSON SHA-256 minus volatile fields.
    fn content_hash(resource) -> Hash { /* default */ }

    // Translate scanned resource if vendor profiles deviate from R5.
    // Default: identity. Override for profile normalization.
    fn normalize(resource) -> FhirResource { /* default: identity */ }
}
```

### `VendorApiClient` — Stage 1 source for proprietary APIs and change feeds

```
abstract class VendorApiClient {
    // === Provided by the framework: ===
    //   - cursor persistence in adapter_state
    //   - reconnect with exponential backoff on connection drops
    //   - in-flight idempotency via correlation_id
    //   - graceful shutdown (drain in-flight events)
    //   - metrics: events_received, connection_state, lag

    // === REQUIRED overrides ===
    // Long-running consumer for the vendor change feed. The base class
    // calls this in a supervised loop; implementors stream events to the
    // provided sink.
    abstract async fn consume(sink: EventSink, cursor) -> ()

    // Translate one vendor-proprietary record to a FHIR resource +
    // change_kind. Called by the base class for each event the consumer
    // pushes onto the sink.
    abstract fn translate(vendor_record) -> ResourceChange
}
```

### `HydrationService` — synchronous resource fetch on demand

```
abstract class HydrationService {
    // === Provided by the framework: ===
    //   - engine callback wiring
    //   - per-replica in-memory LRU cache with short TTL
    //   - request coalescing (multiple concurrent calls for the same ref
    //     deduplicate to one EHR fetch)
    //   - rate-limit budget enforcement
    //   - hard timeout per fetch

    // === REQUIRED override ===
    // Fetch one resource from the EHR by reference.
    // Implementors handle vendor auth, pagination, profile normalization.
    abstract async fn fetch(reference: FhirReference, http) -> FhirResource

    // === OPTIONAL override ===
    fn cache_ttl() -> Duration { /* default: 60s */ }
}
```

## Shared types

### `AdapterContext`

Provided to every sub-component's constructor and to the top-level `EhrAdapter`. The host injects:

```
struct AdapterContext {
    config: AdapterConfig            // validated against manifest schema
    state_store: AdapterStateStore   // KV scoped to this adapter
    http: HttpClient                 // pre-configured: auth, TLS, retries
    metrics: MetricsEmitter
    logger: Logger
    resource_change_sink: Sink       // base classes call this; not for direct use
    tracer: Tracer                   // OpenTelemetry tracer
}
```

The adapter does NOT receive a database connection. All DB writes go through the framework — the adapter writes a `ResourceChange` to `resource_change_sink` and the framework persists it transactionally. The adapter uses `state_store` for its own persistent state.

### `AdapterStateStore`

A scoped KV store. The architecture commits the contract; the SPI shape is roughly:

```
trait AdapterStateStore {
    async fn get(key: &str) -> Option<Bytes>;
    async fn put(key: &str, value: Bytes) -> Result<()>;
    async fn delete(key: &str) -> Result<()>;
    async fn list(prefix: &str) -> Iterator<(String, Bytes)>;

    // Transaction support — used by the FHIR Scan Runner to atomically
    // update a snapshot and emit a resource_change.
    async fn transaction<F>(f: F) -> Result<F::Output>
        where F: AsyncFnOnce(StateStoreTx) -> Result<F::Output>;
}
```

Keys are scoped per adapter (the host prefixes the adapter ID under the hood). An adapter cannot read or write another adapter's state.

State store usage:

- **FHIR Scan Runner** — keys like `snapshot:<resource_type>:<resource_id>` mapping to a content hash plus the last-seen body.
- **Vendor API Client** — keys like `cursor:<feed_name>` for the change-feed cursor.
- **HL7 Message Processor (cancel-and-replace pending)** — managed by the framework, but uses the same state store.

### `ResourceChange`

The output of every Stage 1 sub-component:

```
struct ResourceChange {
    resource_type: String
    change_kind: enum { Create, Update, Delete }
    resource: FhirResource           // the post-translation resource
    previous_resource: FhirResource? // populated for Update and Delete
    occurred_at: Timestamp
    correlation_id: String           // for idempotency and tracing
    event_code: Option<String>       // populated by Vendor API Client when the change came from a tagged change-feed event; consumed by SubscriptionTopic.eventTrigger matching
}
```

The framework writes this to `resource_changes` (see [internal-tables.md](internal-tables.md#resource_changes)) inside the same transaction that marks the source row processed.

### `AdapterManifest`

The manifest the top-level `EhrAdapter` returns:

```
struct AdapterManifest {
    id: String                       // e.g., "epic", "epic-2026-11", "default"
    vendor: String                   // e.g., "Epic Systems Corporation"
    description: String
    supported_ehr_versions: VersionSpec   // e.g., ">=2024.1"

    // Declared capabilities — must match what build_* returns.
    capabilities: {
        hl7_processor: bool,
        fhir_scan_runner: bool,
        vendor_api_client: bool,
        hydration_service: bool,
    }

    // JSON Schema for adapter-specific configuration.
    config_schema: JsonSchema

    // Topics this adapter contributes (canonical URLs + version).
    contributed_topics: [SubscriptionTopic]

    // SPI version this adapter implements.
    spi_version: SemVer
}
```

The host validates `config` against `config_schema` at startup and refuses to start with mismatched configuration. The host validates `capabilities` against `build_*` returns — if `hl7_processor: true` is declared but `build_hl7_processor` returns null, the adapter is misconfigured and startup fails.

`contributed_topics` are merged into the topic catalog at startup per [topics.md](../domains/topics.md).

## Capability declaration

The manifest's `capabilities` is what the host validates the deployment configuration against. Examples:

- An EHR that only emits HL7 v2 returns `capabilities: { hl7_processor: true, fhir_scan_runner: false, vendor_api_client: false, hydration_service: true }` and returns null from the unused `build_*` methods. The host wires nothing for those sub-components and will reject any subscription whose payload type requires hydration if `hydration_service: false` (full-resource is unsupportable without hydration). In practice, every adapter will provide a hydration service even if it provides nothing else, because hydration is required for full-resource subscribers.
- An EHR that only exposes a vendor change feed and a FHIR API returns `capabilities: { hl7_processor: false, fhir_scan_runner: true, vendor_api_client: true, hydration_service: true }`.

The default reference adapter returns `capabilities` based on configuration: HL7 enabled if any listener endpoint is configured, FHIR scan enabled if any `scan_plan` is configured, etc.

## What an implementor actually writes

The architecture's words: "The minimal Epic adapter is roughly: subclass the four base classes, override the methods marked **REQUIRED**, override a handful of **OPTIONAL** methods where Epic differs (Z-segment lex, profile-aware validate, FHIR pagination quirks, vendor change-feed protocol). The base classes handle every cross-cutting concern listed under 'Provided by the framework' so vendor code stays focused on vendor knowledge."

Concretely:

- One file with the `EhrAdapter` subclass: returns `manifest()`, `build_*` constructors, optionally `on_start` for SDK initialization.
- One file per sub-component subclass: overrides the REQUIRED methods plus the OPTIONAL methods that differ from the base.
- One configuration schema file (JSON Schema) declared in the manifest.
- One folder of contributed topics if applicable.

A complete adapter is typically a few hundred lines of vendor logic plus the inherited base behavior.

## Sandboxing

The architecture commits sandboxing as a design principle. Adapters do not have direct access to the database, do not have direct access to the network beyond their declared HTTP client, and do not read environment variables directly. Configuration values that look like environment placeholders are resolved by the host before the adapter sees them.

The host injects:

- An HTTP client pre-configured with auth and TLS.
- The `AdapterStateStore`.
- The `Logger` and `MetricsEmitter`.
- The `Tracer`.
- The `resource_change_sink` (the `record-and-write-to-resource_changes` mechanism the base classes use).

Adapters that want additional capability (e.g., to read files, to open raw sockets) declare it in the manifest and the host enforces it at runtime. Out-of-tree adapters that go around the sandbox are off-spec; the project does not support them.

## Versioning policy for this SPI

The Adapter SPI is the most important stable interface in the project. Stability is a long-term goal.

- **`spi_version` in the manifest** declares which SPI version the adapter implements. The host accepts adapters whose major SPI version matches the host's; mismatched majors cause startup to fail with a clear message.
- **Additive changes** are minor versions: new optional fields on `AdapterContext`, new optional methods with default implementations on a base class, new fields on `ResourceChange` that older readers ignore. Existing adapters continue to work unchanged.
- **Required changes are major.** A new REQUIRED override on a base class, a removed field, a changed method signature — all major. The project commits to releasing the new SPI alongside the old one for a release cycle so out-of-tree adapters have time to migrate.
- **The base classes are part of the SPI.** Their behavior — the queue loop, the cancel-and-replace state machine, the snapshot diffing — is documented and stable. Changes to that behavior that an existing override depends on are major.
- **`AdapterStateStore` keys are private to each adapter.** The host scopes them; an adapter may version its own keys however it wants. The project does not force a key migration discipline.
- **Configuration schema migrations** are an adapter concern. An adapter that changes its config schema between minor SPI versions must accept the old schema and migrate forward at startup, or document the migration the operator must perform.

The SPI does not extend the FHIR Subscriptions spec. Its job is to encapsulate vendor knowledge so the spec-aware core never grows a `match vendor` switch. Anything the spec doesn't speak to — vendor change-feed protocols, vendor authentication, vendor profile quirks — lives entirely behind the SPI. See [decisions/0007-spec-bounded-scope.md](../decisions/0007-spec-bounded-scope.md).
