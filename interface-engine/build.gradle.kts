// Spring Boot + Apache Camel + IPF + HAPI HL7v2 scaffold (ticket #360).
//
// Versions pinned to match IPF 5.2.0's dependencies pom:
//   https://github.com/oehf/ipf/blob/ipf-5.2.0/dependencies/pom.xml
// Bumping IPF requires re-checking those transitive pins.

plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.spring") version "2.0.21"
    id("org.springframework.boot") version "3.5.14"
    id("io.spring.dependency-management") version "1.1.7"
}

// NOTE on JPA + Kotlin: Hibernate needs a no-arg constructor on every
// @Entity for reflective instantiation. The conventional fix is the
// `kotlin("plugin.jpa")` (kotlin-noarg) compiler plugin, but adding it
// would require the Dockerised Gradle build to reach the Gradle plugin
// portal at build time — which fails behind our corporate-managed TLS
// proxy. To keep the Dockerfile unchanged, we instead provide a default
// value for every constructor parameter on JPA entities; Kotlin's
// compiler then synthesizes a no-arg secondary constructor that
// Hibernate can invoke. See IngestedMessage.kt.

group = "com.bzonfhir.subscriptionservice"
version = "0.0.1-SNAPSHOT"

java {
    // Compile to bytecode level 17 so this builds on JDK 17 (developer laptops)
    // AND runs unchanged on the JRE 21 base image in Docker.
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

val ipfVersion = "5.2.0"
val camelVersion = "4.18.2"
val hapiHl7v2Version = "2.6.0"
val hapiFhirVersion = "8.10.0"

// OpenTelemetry (Epic #387, ticket #394). Pinned to the 1.41.x LTS line —
// stable API surface, no breaking changes vs the current main branch's
// Spring Boot 3.5 baseline. We pull in the BOM so every otel-* artifact
// resolves to the same version without us tracking each one separately.
//
// Why NOT the `opentelemetry-spring-boot-starter` artifact:
//   - That starter is on its own version cadence (currently tracks Spring
//     Boot 3.4) and dragging it into our 3.5.x project produces transitive
//     conflicts the BOM can't resolve.
//   - We don't need a fully agent-based instrumentation surface; the only
//     wire-format propagation we care about is W3C traceparent on the
//     handful of outbound HTTP calls the worker makes. A direct SDK
//     dependency + a small RestTemplate interceptor we own is simpler
//     than wiring the starter just to get the SDK on the classpath.
val otelVersion = "1.41.0"

dependencyManagement {
    imports {
        mavenBom("org.apache.camel.springboot:camel-spring-boot-bom:$camelVersion")
        // OTel BOM resolves every io.opentelemetry:* dep to a single
        // coherent version. Without this, transitive deps (e.g. otlp
        // exporter pulling in opentelemetry-api) could end up on
        // mismatched lines and the SDK's internal version-check would
        // emit a warning at boot.
        mavenBom("io.opentelemetry:opentelemetry-bom:$otelVersion")
    }
}

dependencies {
    // plugins-spi (ticket #430, Epic #425) — the public extension surface.
    // Bringing it in here doesn't activate any plugin yet; this dep just
    // gives future stories in Epic #425 a compile-time path to refactor
    // the existing ingest / observability / audit code into SPI-shaped
    // bindings without further wiring changes. The SPI module itself is
    // dep-light (Kotlin stdlib only; HAPI is compileOnly there) so
    // pulling it in doesn't bloat the runtime classpath.
    implementation(project(":plugins-spi"))

    // Built-in HL7 v2 MLLP plugin (ticket #431, Epic #425). The interface
    // engine's receive path used to be inline Camel-MLLP code under
    // .routes.IngestRoutes; that work moved into the plugin module so the
    // SPI is self-demonstrating. Pulling the plugin in here activates its
    // Spring Boot auto-config (see Hl7V2MllpAutoConfiguration.kt) which
    // registers the Hl7V2MllpIngestSource bean — IngestSourceRegistry
    // discovers it and starts it at boot.
    implementation(project(":plugins-builtin:hl7v2-mllp"))

    // Built-in observability enricher plugin (ticket #433, Epic #425). The
    // standard log-field / metric-label catalog moved into this plugin.
    // Transport (this module) hosts the SDK + Prometheus actuator; the
    // plugin owns the "what gets stamped" decisions.
    implementation(project(":plugins-builtin:observability-otel"))

    // Built-in FHIR R4 polling IngestSource plugin (ticket #434, Epic #425).
    // Foundation for the Athena vendor profile in Epic #426 (Athena
    // exposes some data via standard FHIR R4) and any future polling-
    // based source. Pulling the plugin in here activates its Spring
    // Boot auto-config (see FhirPollingAutoConfiguration.kt) which
    // registers one FhirPollingIngestSource bean per configured source
    // — IngestSourceRegistry discovers them and starts them at boot.
    //
    // No sources are configured out of the box; the plugin is dormant
    // until an operator adds entries under
    // `subscription-service.ingest.fhir-polling.sources[]`.
    implementation(project(":plugins-builtin:fhir-polling"))

    // Spring Boot core + actuator for /actuator/health.
    implementation("org.springframework.boot:spring-boot-starter")
    implementation("org.springframework.boot:spring-boot-starter-web")
    implementation("org.springframework.boot:spring-boot-starter-actuator")

    // Micrometer Prometheus registry (Epic #387, ticket #389).
    //
    // Spring Boot 3.5.x bundles micrometer-core (the metrics abstraction) via
    // the actuator starter, but NOT the Prometheus registry — adding it here
    // turns on the `/actuator/prometheus` endpoint when also added to
    // `management.endpoints.web.exposure.include`. We pin via the Spring
    // Boot BOM (`io.spring.dependency-management` resolves Micrometer's
    // version to the one Boot was tested with — 1.14.x for Boot 3.5.x), so
    // no explicit version string is needed and a future Boot bump won't
    // leave Micrometer trailing.
    //
    // Why Prometheus specifically: the chart already plumbs ServiceMonitor
    // CRDs (ticket #418 / values.yaml `monitoring.enabled`) for the
    // Prometheus Operator. Picking Prometheus here means zero scrape-config
    // changes for operators who already run that stack.
    implementation("io.micrometer:micrometer-registry-prometheus")

    // Kotlin.
    implementation("org.jetbrains.kotlin:kotlin-reflect")
    implementation("org.jetbrains.kotlin:kotlin-stdlib")

    // IPF: Spring Boot autoconfig + HL7v2 DSL on top of Camel.
    //
    // We avoid pulling `ipf-hl7-spring-boot-starter` because it transitively
    // registers IPF's IHE custom-MLLP component, which hijacks the `mllp://`
    // URI scheme and refuses to start without an IHE `hl7TransactionConfig`.
    // Our scaffold uses *Camel's* generic MLLP component instead (camel-mllp
    // below). We still get the IPF HL7v2 DSL via `ipf-platform-camel-hl7`.
    implementation("org.openehealth.ipf.boot:ipf-spring-boot-starter:$ipfVersion")
    implementation("org.openehealth.ipf.platform-camel:ipf-platform-camel-hl7:$ipfVersion")

    // Camel components used directly by routes / tests.
    implementation("org.apache.camel.springboot:camel-mllp-starter")
    implementation("org.apache.camel.springboot:camel-hl7-starter")
    // camel-http for the Matchbox $transform POST. We use Camel's HTTP
    // component (Apache HttpComponents v5 under the hood) instead of Spring's
    // RestTemplate/WebClient so the call participates in Camel's error
    // handler, retries, and timeouts — and so the same Exchange carries the
    // HL7 v2 control id, message type, etc. straight through to ACK logic.
    implementation("org.apache.camel.springboot:camel-http-starter")

    // HAPI HL7v2 structures (v2.5 covers ADT^A01 used here).
    implementation("ca.uhn.hapi:hapi-base:$hapiHl7v2Version")
    implementation("ca.uhn.hapi:hapi-structures-v25:$hapiHl7v2Version")

    // HAPI FHIR client + R4 structures — plumbed for ticket #361 (not yet wired).
    implementation("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    implementation("ca.uhn.hapi.fhir:hapi-fhir-client:$hapiFhirVersion")
    implementation("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")

    // Postgres-backed durable inbound store (Epic #378). The interface engine
    // owns its own database ("ipf" by default) on the same Postgres SERVER
    // that HAPI uses, but a separate Spring datasource + Flyway migration
    // history. Subsequent stories add JPA repositories on top of this base.
    implementation("org.springframework.boot:spring-boot-starter-data-jpa")
    implementation("org.springframework.boot:spring-boot-starter-jdbc")
    implementation("org.postgresql:postgresql:42.7.4")
    implementation("org.flywaydb:flyway-core:10.20.1")
    implementation("org.flywaydb:flyway-database-postgresql:10.20.1")

    // JSON-formatted logs (Epic #387, ticket #388). logstash-logback-encoder
    // produces one JSON object per log record in the Logstash / ECS-style
    // layout, with MDC values surfaced as top-level fields and exception
    // stack traces emitted as a `stack_trace` string. Picked over
    // `logback-jackson` because logstash-logback-encoder is the dominant
    // choice in the Spring Boot world and integrates cleanly with SLF4J
    // MDC for correlation-id propagation. Version 7.4 is the latest stable
    // line compatible with Logback 1.5.x (which Spring Boot 3.5 brings).
    implementation("net.logstash.logback:logstash-logback-encoder:7.4")

    // OpenTelemetry SDK (Epic #387, ticket #394).
    //
    // Three artifacts, all on the OTel BOM so no explicit versions:
    //
    //   - opentelemetry-api: the public `Tracer` / `Span` / `SpanBuilder`
    //     surface our code uses directly. Always-on (api is a no-op when
    //     the SDK is disabled).
    //   - opentelemetry-sdk: the SDK that actually records spans + holds
    //     the configured TracerProvider / propagators. The autoconfigure
    //     module below builds one from environment variables.
    //   - opentelemetry-exporter-otlp: the OTLP gRPC + HTTP exporter that
    //     ships spans to a collector. Only sends when
    //     OTEL_EXPORTER_OTLP_ENDPOINT is set AND OTEL_SDK_DISABLED is unset
    //     (or false). The default config produces a no-op SDK that never
    //     opens a network connection, which is what we want for operators
    //     who don't run a collector.
    //   - opentelemetry-sdk-extension-autoconfigure: parses OTEL_* env vars
    //     into an SdkTracerProvider, wires the OTLP exporter, picks the
    //     W3C traceparent propagator by default. One bean, one call to
    //     AutoConfiguredOpenTelemetrySdk.initialize(), and we're done.
    implementation("io.opentelemetry:opentelemetry-api")
    implementation("io.opentelemetry:opentelemetry-sdk")
    implementation("io.opentelemetry:opentelemetry-exporter-otlp")
    implementation("io.opentelemetry:opentelemetry-sdk-extension-autoconfigure")

    // Tests.
    testImplementation("org.springframework.boot:spring-boot-starter-test")
    // Apache HttpComponents 5 — needed so TestRestTemplate's
    // HttpComponentsClientHttpRequestFactory can issue PATCH requests
    // (ticket #404). The default JDK-backed client factory does NOT
    // support PATCH; without this dep `rest.exchange(..., HttpMethod.PATCH, ...)`
    // throws "Invalid HTTP method: PATCH".
    testImplementation("org.apache.httpcomponents.client5:httpclient5")
    testImplementation("org.apache.camel:camel-test-spring-junit5:$camelVersion")
    testImplementation("org.apache.camel:camel-test-junit5:$camelVersion")
    testImplementation("org.awaitility:awaitility:4.2.2")
    testImplementation("org.mockito.kotlin:mockito-kotlin:5.4.0")
    // Testcontainers for Postgres-backed Flyway / JPA tests. Optional —
    // skipped if Docker isn't reachable; the Spring context tests still
    // run against H2 in-memory (Flyway has a PostgreSQL-flavor SQL we
    // gate by profile).
    testImplementation("org.testcontainers:testcontainers:1.20.3")
    testImplementation("org.testcontainers:postgresql:1.20.3")
    testImplementation("org.testcontainers:junit-jupiter:1.20.3")

    // OTel in-memory test exporter (ticket #394). Captures emitted spans
    // into a list so OtelTraceTest can assert on their attributes,
    // parents, and names without standing up a real collector. The
    // artifact is OTel-side test-scope only — production runtime uses
    // the OTLP exporter via the autoconfigure SDK.
    testImplementation("io.opentelemetry:opentelemetry-sdk-testing")
}

tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile> {
    compilerOptions {
        freeCompilerArgs.add("-Xjsr305=strict")
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

tasks.withType<Test> {
    useJUnitPlatform()
    // Testcontainers on Rancher Desktop: the Ryuk resource-reaper container
    // can't bind-mount the Docker socket (/Users/$USER/.rd/docker.sock isn't
    // a regular file from k3s/moby's view), so disable it. Containers we
    // start will instead be cleaned up via JVM shutdown hooks. Equivalent
    // to setting ryuk.disabled=true in ~/.testcontainers.properties — we
    // push it into the test JVM's env explicitly so CI behaves the same way
    // as the developer machine.
    environment("TESTCONTAINERS_RYUK_DISABLED", "true")
}
