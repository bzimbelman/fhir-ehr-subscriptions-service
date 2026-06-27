# plugins-spi

The PUBLIC backend extension surface for `subscription-service`.

Ticket #430, Epic #425 (foundational story).

This module contains ONLY interfaces and supporting value types. There is no runtime, no Spring auto-configuration, no Camel routes here. The runtime that discovers and binds plugin implementations lives in `interface-engine/` and `hapi/auth/`. We keep this module tiny on purpose: a plugin author depending on `plugins-spi` should not inherit a transitive Spring / IPF / Camel version chain.

## The eight SPI surfaces

| # | Interface | What plugs in |
|---|---|---|
| 1 | `HL7VendorProfile` | Vendor profile manifest binding (Epic, Athena, Meditech…). Loaded from `manifest.yaml` plus optional Java hooks. |
| 2 | `IngestSource` | Pluggable inbound source — MLLP, FHIR R4 polling, Athena native REST, CSV folder drop, etc. |
| 3 | `MessageSink` | Where transformed Subscription events can be routed besides the FHIR-native channels — Kafka, S3, custom REST, HL7 outbound. |
| 4 | `SubscriptionFilter` | Programmatic filter beyond FHIR criteria — ML scoring, time-of-day, custom routing. |
| 5 | `AuthorizationDecider` | Pluggable authz beyond OIDC roles — LDAP, Azure AD, Okta SSO, customer entitlement tables. |
| 6 | `StorageBackend` | FHIR storage layer. HAPI JPA is the default; alternatives plug here (Elasticsearch, sharded RDBMS, in-memory). |
| 7a | `ObservabilityEnricher` | Add fields to JSON logs, add labels to Prometheus metrics. |
| 7b | `AuditEventEnricher` | Add fields to emitted FHIR `AuditEvent` resources. |
| 8 | `UiExtension` (NOT in this module) | UI extension points — defined in TypeScript at `@bzonfhir/ui-extensions` on npm. See master plan §3.2.1. |

The eighth surface (UI) is intentionally NOT in this module — it lives on the TypeScript side because the operator UI is a Next.js app. A sibling story in Epic #425 adds the TS package.

## Stability tiers

Every interface and value type in `plugins-spi` is tagged with a stability level. v1.0 of the module ships everything as **EXPERIMENTAL**.

- **EXPERIMENTAL** — shape may change without warning between minor versions. Plugin authors should pin to an exact `plugins-spi` version and re-test on each bump. Default tier for new SPI surfaces.
- **STABLE** — shape changes only at major version bumps. We commit to compile compatibility within a major. A surface graduates from EXPERIMENTAL to STABLE only after two minor releases without breaking changes, ideally with at least one third-party implementation in the wild.
- **DEPRECATED** — slated for removal. Plugins using a DEPRECATED interface should migrate; the interface stays compilable for one major version after deprecation, then is removed.

## Semver policy

`plugins-spi` follows strict semver (master plan §4.2 / §8 governance):

- **MAJOR** (`1.x.y` → `2.0.0`) — any binary-incompatible change to a STABLE interface: removed method, changed signature, added abstract method without a default, removed type. Allowed only at major bumps. Plugin authors MUST re-test on a major bump.
- **MINOR** (`1.1.x` → `1.2.0`) — any backward-compatible additions: new interface, new data class, new optional method on an EXPERIMENTAL interface, new value in an open enum-shaped sealed class.
- **PATCH** (`1.1.1` → `1.1.2`) — bug fixes in documentation, KDoc clarifications, no signature changes.

Breaking changes to EXPERIMENTAL surfaces are allowed at any MINOR; the experimental tag is the warning.

## How to author a plugin — one example per SPI tier

Plugins are plain JARs that ServiceLoader picks up at boot. The runtime expects `META-INF/services/<interface FQN>` entries pointing at your implementation class.

### Built-in plugin (lives in this repo)

The default HL7 v2 MLLP listener (`plugins-builtin/hl7v2-mllp/`, comes in a sibling story) implements `IngestSource`:

```kotlin
class Hl7v2MllpIngestSource(...) : IngestSource {
    override val meta = PluginMeta("hl7v2-mllp", "1.0.0", 1, PluginSupplier.FIRST_PARTY, "Default HL7 v2 MLLP listener")
    override val protocol = "hl7v2-mllp"
    override fun start(callback: (PipelineMessage) -> Unit) { /* spin up Camel route */ }
    override fun stop() { /* tear down */ }
}
```

Bundled directly into `subscription-service.jar`; no separate artifact.

### Vendor profile (mostly declarative)

A vendor profile is typically a `manifest.yaml` + StructureMaps; the runtime synthesizes an `HL7VendorProfile` from the YAML (see master plan §4.3). For the rare case where YAML isn't enough, ship a JAR implementing `HL7VendorProfile`:

```kotlin
class EpicProfile : HL7VendorProfile {
    override val meta = PluginMeta("epic", "2024.1.0", 1, PluginSupplier.COMMERCIAL, "Epic vendor profile")
    override val supportedMessageTypes = setOf("ADT^A04", "ORM^O01", "ORU^R01")
    override val quirks = mapOf("msh3-format" to "facility-shortcode-then-pipe")
    override val auditEnrichments = listOf(AuditEnrichmentRule("addOriginatingUser", "pv1.7"))
    override fun mapMessageToFhir(raw: ByteArray, contentType: String): FhirMappingResult { /* ... */ }
}
```

Dropped as `epic-profile-2024.1.0.jar` into `/plugins/profiles/`.

### Community JAR drop-in (any of the seven surfaces)

A community Kafka sink:

```kotlin
class KafkaSink(private val producer: KafkaProducer<String, String>) : MessageSink {
    override val meta = PluginMeta("kafka-sink", "1.0.0", 1, PluginSupplier.COMMUNITY, "Kafka outbound sink")
    override fun handle(event: SubscriptionEvent): SinkOutcome =
        try {
            val md = producer.send(ProducerRecord("subscription-events", event.eventId, event.resourceJson)).get()
            SinkOutcome.Delivered(externalId = "${md.partition()}-${md.offset()}")
        } catch (e: Exception) {
            SinkOutcome.Failed(reason = e.message ?: e.javaClass.simpleName, retryable = true)
        }
}
```

Dropped as `kafka-sink-1.0.0.jar` into `/plugins/`.

## What's NOT in this module

- **No HAPI repackage.** HAPI types (e.g. `AuditEvent`) are `compileOnly` deps; consumers bring their own HAPI version. This eliminates "plugins-spi bundled HAPI X but I want HAPI Y" version skew.
- **No Spring annotations.** `@Component`, `@Configuration`, etc. would couple the SPI to a specific Spring version. Plain interfaces let plugin authors use whatever DI / lifecycle they prefer.
- **No WASM / scripting runtime.** Plugins are JARs. Period. See master plan §3.3 and §4.4.

## Validation

- `./gradlew :plugins-spi:build` — runs all eight contract tests.
- Each contract test instantiates a no-op anonymous implementation of one SPI surface and exercises every member.
- The tests COMPILE-time-lock the SPI shape: a binary-incompatible change fails the test compile.

## File layout

```
plugins-spi/
├── build.gradle.kts                              Kotlin library, JVM 17
├── README.md                                     This file
└── src/
    ├── main/kotlin/com/bzonfhir/subscriptionservice/spi/
    │   ├── package-info.kt                       Module overview
    │   ├── HL7VendorProfile.kt                   SPI #1
    │   ├── IngestSource.kt                       SPI #2
    │   ├── MessageSink.kt                        SPI #3
    │   ├── SubscriptionFilter.kt                 SPI #4
    │   ├── AuthorizationDecider.kt               SPI #5
    │   ├── StorageBackend.kt                     SPI #6
    │   ├── ObservabilityEnricher.kt              SPI #7a
    │   ├── AuditEventEnricher.kt                 SPI #7b
    │   └── meta/                                 Shared value types
    │       ├── PluginMeta.kt                     id, version, schemaVersion, supplier
    │       ├── PipelineMessage.kt                Canonical in-flight message
    │       ├── SubscriptionEvent.kt              Events + outcomes
    │       ├── AuthDecision.kt                   Allow|Deny|Abstain + Principal
    │       └── AuditContext.kt                   Audit + observability + storage types
    └── test/kotlin/com/bzonfhir/subscriptionservice/spi/
        ├── HL7VendorProfileContractTest.kt
        ├── IngestSourceContractTest.kt
        ├── MessageSinkContractTest.kt
        ├── SubscriptionFilterContractTest.kt
        ├── AuthorizationDeciderContractTest.kt
        ├── StorageBackendContractTest.kt
        ├── ObservabilityEnricherContractTest.kt
        └── AuditEventEnricherContractTest.kt
```
