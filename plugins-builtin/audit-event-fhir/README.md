# audit-event-fhir

First built-in plugin for subscription-service (ticket #432, epic #425).

Re-expresses the in-tree HAPI `AuditEventInterceptor` (in `hapi/auth/`) as a Kotlin plugin that implements the `AuditEventEnricher` SPI surface. No behavioural change: every emitted `AuditEvent` has the same shape, the same `type`/`subtype`/`action`/`agent`/`entity`/`source`/`period`/`outcome` fields, and the same skip-list / opt-in toggles the legacy interceptor uses.

## What it does

The plugin's `AuditEventFhirEnricher` is a pure function: given an `AuditContext` (the SPI's normalised request shape) and a fresh `AuditEvent`, return the AuditEvent populated with the FHIR DICOM-rest shape:

- `type` = DICOM `rest` ("Restful Operation").
- `subtype` = a `restful-interaction` coding derived from the operation (`create`, `read`, `update`, `delete`, `patch`, `search`, `history`, `transaction`, `batch`).
- `action` = `C` / `R` / `U` / `D` / `E` (the IHE convention).
- `recorded` = now.
- `outcome` = derived from the response status (`>=500` → `_8`, `>=400` → `_4`, else `_0`) or the exception status (`401` → `_8`, other 4xx → `_4`).
- `agent` = the authenticated principal (when present) PLUS the OAuth client `azp` (when present). Falls back to an anonymous placeholder.
- `source.site` = the FHIR server base URL.
- `entity.what` = `Reference("ResourceType/id")` when both are known, display-only when only the type is known, skipped entirely on system-level operations.
- `period` = `[ctx.occurredAt, now]`.

Vendor enrichment rules (master plan §4.3) are supported via an overload: `enrich(ctx, baseEvent, rules)`. Today one rule key is recognised — `addOriginatingUser` — which reads `ctx.attributes["enrichment.originatingUser"]` and stamps it as an extra `agent.who = Practitioner/<value>`. Unknown rule keys silently no-op so a vendor profile declaring a rule a future runtime hasn't learned doesn't fail.

## Configuration

Bound under `subscription-service.audit` — same keys as the legacy `AuditProperties` in `hapi/auth/`:

```yaml
subscription-service:
  audit:
    enabled: true            # master toggle (default: true)
    capture-reads: false     # also audit successful reads (default: false)
    capture-search: false    # also audit successful searches (default: false)
    retention-days: 365      # informational; purging is a separate scheduled job
```

Per the SPI contract, the master toggle gates the whole enricher bean: when `enabled=false`, no `AuditEventEnricher` is published into the application context (`@ConditionalOnProperty`).

## How it's wired today (hapi-auth)

The `hapi/auth/` Maven module continues to own the HAPI pointcut wiring:

- `AuditEventInterceptor.java` registers on `Pointcut.SERVER_OUTGOING_RESPONSE` + `Pointcut.SERVER_HANDLE_EXCEPTION`.
- It builds an `AuditContext` from the live `RequestDetails` / `ResponseDetails`.
- It delegates AuditEvent construction to the SPI-shaped enricher chain (same logic that lives in this plugin module, mirrored as a Java class so the Maven build stays Gradle-free — see "Maven/Gradle interop" below).
- It hands the result to the existing `AuditEventPersister` (`DaoRegistryAuditEventPersister` in prod).

## Maven/Gradle interop

The `hapi/auth/` module uses Maven; this plugin module uses Gradle. The Docker build (`hapi/Dockerfile`) copies `hapi/auth/` into a Maven container and runs `mvn package` — it has no access to artefacts a Gradle build would produce.

**For ticket #432**, we keep the runtime path simple: the SPI-shaped enricher logic lives canonically in this Kotlin module (tested independently), and a parallel Java implementation in `hapi/auth/com/bzonfhir/subscriptionservice/audit/` mirrors it. Both produce the same AuditEvent for the same input; both are covered by tests. The mirroring is small (a handful of pure functions); when `hapi/auth/` migrates to Gradle in a follow-up story it collapses into a single `implementation(project(":plugins-builtin:audit-event-fhir"))` dependency and the Java mirror is deleted.

To consume the plugin from a Gradle-based Spring Boot app today:

```kotlin
dependencies {
    implementation(project(":plugins-builtin:audit-event-fhir"))
}
```

The auto-configuration (`AuditEventFhirAutoConfiguration`) registers an `AuditEventEnricher` bean. Any caller that injects `ObjectProvider<AuditEventEnricher>` walks every registered enricher in turn — that is exactly how the (refactored) hapi-auth interceptor will discover and run this plugin once both modules share a build.

## Tests

- `AuditEventFhirEnricherTest` — SPI contract: every operation maps to the correct `action`, agents are stamped correctly, entity reference shape is right, vendor `addOriginatingUser` rule adds an extra agent.
- `AuditEventBuildersTest` — unit tests for the pure builder helpers (subtype/action/outcome mappings, agent stuffing, entity stuffing, source stuffing).
- `AuditEventEmitterIntegrationTest` — Spring autoconfig wiring: the bean appears, configuration properties bind, the master toggle disables the bean.

Run them: `./gradlew :plugins-builtin:audit-event-fhir:test`

The end-to-end "AuditEvent is actually persisted into a live HAPI server" verification continues to be owned by `hapi/auth/`'s `AuditEventInterceptorTest`, which exercises the full interceptor+persister path with a HAPI in-memory request shape. The behaviour-equivalence claim (this plugin and the Java mirror produce identical AuditEvents) is verified by both modules' assertions covering the same shapes.

## Schema

This plugin implements SPI surface `AuditEventEnricher` at `schemaVersion = 1`. The `meta` field exposes:

- `id` = `"audit-event-fhir"`
- `version` = `"0.1.0"`
- `schemaVersion` = `1`
- `supplier` = `FIRST_PARTY`

When the SPI surface adds a binary-incompatible change, the `schemaVersion` bumps and the runtime refuses to load plugins whose declared schemaVersion no longer matches.
