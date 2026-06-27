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

// plugins-builtin/observability-otel (ticket #433, Epic #425). Re-expresses
// the standard log-field / metric-label catalog as an
// `ObservabilityEnricher` plugin. The transport (OTel SDK, Prometheus
// actuator endpoint, Logback JSON encoder) stays in interface-engine as
// infrastructure; the plugin only owns the "what gets stamped" decisions.
include("plugins-builtin:observability-otel")
project(":plugins-builtin:observability-otel").projectDir =
    file("plugins-builtin/observability-otel")
