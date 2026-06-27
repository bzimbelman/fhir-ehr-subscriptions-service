// Root Gradle settings — multi-project build for the subscription-service backend.
//
// Created by ticket #430 (Epic #425, plugin SPI foundational story).
//
// Before #430 the repo had two standalone build trees:
//   - `interface-engine/` — its own Gradle project (settings + wrapper).
//   - `hapi/auth/`        — its own Maven project (pom.xml).
//
// Adding `plugins-spi/` as a third top-level module makes the standalone
// layout awkward: interface-engine wants to depend on plugins-spi, and
// the cleanest way is `project(":plugins-spi")` — which requires a root
// Gradle build that includes both.
//
// What this file does:
//   - `rootProject.name = "subscription-service"` — the project name
//     when Gradle is run from this directory.
//   - `include("plugins-spi", "interface-engine")` — both Kotlin/JVM
//     modules participate in one multi-project Gradle build.
//
// What this file does NOT do:
//   - hapi/auth is NOT a Gradle subproject — it stays on Maven. Its
//     pom.xml is the source of truth; a future story may migrate it to
//     Gradle for consistency but that's out of scope here.
//
// Build invocations:
//   - From this directory: `./gradlew :plugins-spi:build` or
//     `./gradlew :interface-engine:build`.
//   - The interface-engine Dockerfile now builds with the repo root as
//     its Docker context (updated as part of this ticket) so the
//     plugins-spi sources are available to the multi-project build
//     inside the container.

rootProject.name = "subscription-service"

include("plugins-spi", "interface-engine")

// Built-in plugins (Epic #425).
//
// Each built-in plugin lives under plugins-builtin/<id>/ and is its own
// Gradle module so its dependency footprint stays self-contained — the
// MLLP plugin pulls in Camel + HAPI v2; the observability plugin pulls in
// only plugins-spi; the audit plugin pulls in HAPI FHIR R4. Spring Boot
// auto-config under each plugin's META-INF/spring/ wires the plugin into
// the interface-engine runtime when the plugin's JAR is on the classpath.
//
// Ticket #431 — HL7 v2 MLLP listener as an IngestSource plugin.
include("plugins-builtin:hl7v2-mllp")
project(":plugins-builtin:hl7v2-mllp").projectDir = file("plugins-builtin/hl7v2-mllp")

// Ticket #433 — OTel + Prometheus enrichment as an ObservabilityEnricher plugin.
// Transport stays in interface-engine; the plugin owns "what gets stamped".
include("plugins-builtin:observability-otel")
project(":plugins-builtin:observability-otel").projectDir =
    file("plugins-builtin/observability-otel")

// Ticket #432 — AuditEvent emission as an AuditEventEnricher plugin. The
// HAPI AuditEventInterceptor (Maven module hapi/auth) delegates to a Java
// mirror of this plugin's contract; canonical implementation lives here.
include("plugins-builtin:audit-event-fhir")
project(":plugins-builtin:audit-event-fhir").projectDir =
    file("plugins-builtin/audit-event-fhir")

// Ticket #434 — FHIR R4 polling as an IngestSource plugin. Foundation
// for the Athena vendor profile in Epic #426 (Athena exposes some data
// via standard FHIR R4) and any future polling-based source. Multiple
// configured sources per plugin instance (one per entry in `sources[]`)
// — a customer can poll Observation, Encounter, and DocumentReference
// on independent cadences.
include("plugins-builtin:fhir-polling")
project(":plugins-builtin:fhir-polling").projectDir =
    file("plugins-builtin/fhir-polling")
