# plugins-builtin/fhir-polling

Built-in FHIR R4 polling `IngestSource` implementation for `subscription-service`.

Ticket #434, Epic #425 (built-in plugin refactor — second ingest plugin, sibling to `hl7v2-mllp`).

## What it does

Periodically queries a FHIR R4 server for new or changed resources and hands each one to the SPI-provided callback as a canonical `PipelineMessage`. The interface-engine wires that callback into its persist pipeline (`IngestPersistService`), so a polled resource ends up as a row in `ingested_messages` exactly the way an MLLP-delivered HL7 v2 message does.

This is the foundation for the Athena vendor profile (Epic #426 — Athena exposes some data via standard FHIR R4) and any future polling-based source: lab back-end APIs, registry APIs, partner FHIR endpoints.

## How it plugs in

The module's `FhirPollingAutoConfiguration` is listed in `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports` — Spring Boot 3's plugin-discovery descriptor. Any `@SpringBootApplication` host that has this JAR on its classpath picks the configuration up automatically.

The interface-engine host:

1. Discovers each `FhirPollingIngestSource` bean (one per configured polling source) alongside the `Hl7V2MllpIngestSource`.
2. On `ApplicationReadyEvent`, calls `.start(callback)` on each, where the callback funnels the `PipelineMessage` into `IngestPersistService.persistReceived(...)`.
3. On JVM shutdown (`@PreDestroy`), calls `.stop()` on each to halt the scheduler and let the in-flight tick finish.

## Config

All knobs live under `subscription-service.ingest.fhir-polling` and bind to `FhirPollingProperties`.

```yaml
subscription-service:
  ingest:
    fhir-polling:
      enabled: true                    # master switch — default true
      sources:
        - id: "athena-observations"    # unique within a host
          enabled: true                # per-source switch
          base-url: "https://api.platform.athenahealth.com/fhir/r4"
          poll-interval-seconds: 60
          search: "Observation?_lastUpdated=gt{{lastRun}}"
          source-system: "athena"      # used as PipelineMessage.sourceSystem
          auth:
            type: "oauth2-client-credentials"
            token-url: "https://api.athenahealth.com/oauth2/v1/token"
            client-id-env: "ATHENA_CLIENT_ID"
            client-secret-env: "ATHENA_CLIENT_SECRET"
            scope: "system/Observation.read"
        - id: "lab-encounters"
          base-url: "https://lab.example.com/fhir"
          poll-interval-seconds: 300
          search: "Encounter?_lastUpdated=gt{{lastRun}}"
          source-system: "lab-x"
          auth:
            type: "bearer-static"
            token-env: "LAB_X_BEARER_TOKEN"
        - id: "dev-local"
          base-url: "http://localhost:8080/fhir"
          poll-interval-seconds: 30
          search: "Patient?_lastUpdated=gt{{lastRun}}"
          source-system: "local"
          auth:
            type: "none"
```

### Property reference

| Property | Default | What it does |
|---|---|---|
| `…fhir-polling.enabled` | `true` | Master switch. False -> no beans, no schedulers. |
| `…sources[].id` | (required) | Stable id. Unique within a host. Used as bean name + high-water-mark key + plugin id suffix. |
| `…sources[].enabled` | `true` | Per-source switch. False -> bean exists, scheduler is parked. |
| `…sources[].base-url` | (required) | FHIR server base URL. |
| `…sources[].poll-interval-seconds` | `60` | Time between polls. fixedDelay semantics. |
| `…sources[].search` | (required) | FHIR search expression. `{{lastRun}}` is replaced with the high-water mark. |
| `…sources[].source-system` | (required) | Stamped on `PipelineMessage.sourceSystem`. Part of the engine's idempotency key. |
| `…sources[].auth.type` | `none` | One of: `none`, `bearer-static`, `oauth2-client-credentials`. |
| `…sources[].auth.token-env` | `""` | For `bearer-static` — env var name with the bearer value. |
| `…sources[].auth.token-url` | `""` | For `oauth2-client-credentials` — token endpoint. |
| `…sources[].auth.client-id-env` | `""` | For `oauth2-client-credentials` — env var with client id. |
| `…sources[].auth.client-secret-env` | `""` | For `oauth2-client-credentials` — env var with client secret. |
| `…sources[].auth.scope` | `""` | For `oauth2-client-credentials` — optional `scope` parameter. |

### `{{lastRun}}` substitution

The literal seven-character string `{{lastRun}}` in the `search` value is replaced by the high-water-mark store's value for that source's id. The store starts at `1970-01-01T00:00:00Z` for a fresh JVM. After each successful poll the mark advances to the most recent `Resource.meta.lastUpdated` in the returned Bundle. Multiple `{{lastRun}}` occurrences in one search are all replaced with the same value.

### Authentication strategies

Three types are supported in v1:

- **`none`** — no `Authorization` header sent. Local dev, unsecured FHIR servers, integration tests.
- **`bearer-static`** — adds `Authorization: Bearer <env value>` on every request. The bearer is the value of the env var named by `token-env`. Captured at bean-construction time; rotating the secret requires a redeploy.
- **`oauth2-client-credentials`** — performs RFC 6749 §4.4 client-credentials grant against `token-url`, caches the access token in memory (per source), refreshes when within 30s of expiry. Client id/secret resolved from env vars. Matches the Athena platform auth flow described in Epic #426.

Each polling source has its own auth strategy + its own token cache (when applicable). Two sources sharing the same OAuth2 server still maintain independent caches — they may need different scopes.

## What's emitted

Each `PipelineMessage`:

- `correlationId` — fresh UUID v4 per resource (FHIR has no on-wire correlation header).
- `sourceProtocol = "fhir-r4-polling"` — pinned constant.
- `sourceSystem` — the configured `source-system`.
- `sourceId` — the FHIR resource's logical id (`Observation.id`, etc.). Combined with `sourceSystem`, this is the engine's idempotency key.
- `raw` — the resource's HAPI-encoded JSON body (UTF-8 bytes).
- `contentType = "application/fhir+json"`.
- `attributes` — namespaced keys:
  - `fhir.resourceType` (e.g. `"Observation"`).
  - `fhir.resourceId` — duplicates `sourceId` for self-description.
  - `fhir.pollingSourceId` — the configured `id` (e.g. `"athena-observations"`). Lets downstream code disambiguate which source delivered the resource.
  - `fhir.lastUpdated` — ISO-8601 instant.
  - `hl7.messageType` — **compatibility shim**, set to the FHIR resource type. The engine's `IngestSourceRegistry` (pre-#434 code) requires this attribute to be non-empty so it can stamp the row's `message_type` column. A follow-up ticket will refactor the registry to be source-agnostic; until then the shim lets the FHIR plugin coexist with the HL7 v2 plugin in the same host without registry changes.

## V1 limitations

- **High-water marks are in-memory.** A JVM restart forgets the marks and the first poll for each source returns "everything since the epoch." Idempotency downstream (the `(source_system, source_id)` unique constraint on `ingested_messages`) absorbs the duplicates — every resource that was already persisted gets a "duplicate receive" log line and no new row. A follow-up ticket will swap this in-memory store for a JPA-backed version; the interface (`getMark` / `updateMark`) stays the same.
- **No paging.** The plugin reads `Bundle.entry` from the first response only. If the FHIR server returns paged results with a `next` link, only page 1 is delivered. For most polling cadences this is fine — the next tick picks up what page 1 missed. A future ticket can add follow-`next` if needed.
- **No FHIR _include / _revinclude support.** The search expression goes to the server unchanged; included resources arrive in `Bundle.entry` and ARE emitted as PipelineMessages — but the relationship between them is not preserved beyond what `fullUrl` carries. Downstream mapping is the right layer to reassemble graphs.
- **OAuth2 client-credentials only.** No refresh-token, no JWT-bearer, no mTLS. Athena's docs describe client-credentials as the standard path; other vendors with bespoke flows will need additional auth types.

## Troubleshooting

### Plugin doesn't poll on a host that's supposed to have it

Check:
1. `subscription-service.ingest.fhir-polling.enabled` isn't set to `false`.
2. At least one source is listed under `…fhir-polling.sources[]` with valid `id`, `base-url`, `search`, `source-system`.
3. `ca.uhn.fhir.rest.client.api.IGenericClient` is on the host's classpath (the auto-config is gated on this — see `@ConditionalOnClass`).
4. Boot logs include `"configuring fhir polling source id=… baseUrl=… authType=…"` and `"starting fhir polling source id=… interval=…s …"`.

### Polls keep failing with 401 / 403

For `bearer-static`: verify the env var named by `token-env` is set and contains a fresh token. The interceptor captures the value at bean-construction time — a token rotation requires a pod restart.

For `oauth2-client-credentials`: verify `token-url` is correct, the `client-id-env` + `client-secret-env` env vars are set, and (where required) `scope` matches what the server expects. Boot logs include `"oauth2 token expired or near expiry; refreshing from …"` on each token fetch; an HTTP failure logs `"oauth2 token endpoint … returned HTTP …"`.

### Same resources delivered repeatedly

Most likely the high-water mark isn't advancing. Possible causes:

- The Bundle responses have no `meta.lastUpdated` on the entries. The plugin's mark-advance step skips entries without a lastUpdated; a Bundle whose entries ALL lack it leaves the mark at the previous value. Workaround: check whether the upstream FHIR server is stripping `meta.lastUpdated` — it shouldn't.
- The downstream callback is throwing for every message. The scheduler catches the thrown exception and doesn't advance the mark. Boot logs include `"tick failed for source id=…"` followed by the cause.

### Polling source falls behind

If `poll-interval-seconds` is too short for the server's response time, the fixedDelay scheduler will still serialize the polls — but each poll will only see the resources newer than the last successful one. A slow server doesn't cause data loss, just latency. Raise the interval if a slow server is congesting the scheduler thread.

## Test suite

| File | Scope |
|---|---|
| `HighWaterMarkStoreTest` | Pure store: get, update, multi-key, forward-only, race. 6 tests. |
| `FhirSearchExecutorTest` | URL substitution + bundle parsing helpers (mocked HAPI client). 7 tests. |
| `FhirPollingIngestSourceTest` | SPI lifecycle, callback dispatch, mark advancement (mocked HAPI client). 8 tests. |
| `FhirPollingEndToEndTest` | Embedded HTTP server returns FHIR JSON; real HAPI `IGenericClient` polls it; verifies callback fires + mark advances. 1 test. |

Run with `./gradlew :plugins-builtin:fhir-polling:test`.

## Why fhir-r4-polling is its own plugin, not part of hl7v2-mllp

Different transport, different vocabulary, different failure modes. The HL7 v2 plugin's responsibility is "bytes off a socket, MSH-3/9/10 -> PipelineMessage." This plugin's is "scheduled FHIR search -> PipelineMessage per Bundle entry." Sharing module boundaries would force a host that wants only one of them to bring the dependencies of both. Keeping them in sibling modules is what Epic #425's plugin-host refactor is for.
