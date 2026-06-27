// plugins-builtin/observability-otel — the built-in ObservabilityEnricher.
//
// Ticket #433 (Epic #425). Re-expresses the standard "what gets stamped
// onto every log line and every metric" decisions as an
// [com.bzonfhir.subscriptionservice.spi.ObservabilityEnricher]
// implementation. The transport — OTel SDK init, Logback JSON encoder,
// Prometheus actuator endpoint, scheduled gauge poller — stays in the
// interface-engine module as infrastructure.
//
// This plugin's job is narrow and pure:
//
//   1. Given an [ObservabilityContext], compute the standard log fields
//      every JSON log line gets (`schema_version`, `correlation_id`,
//      `trace_id`, `span_id`, `source_protocol`, `source_system`,
//      `message_type`).
//   2. Given a metric series name and an [ObservabilityContext], compute
//      the bounded-cardinality labels that metric supports per the
//      catalog at `docs/observability/metric-catalog.md`.
//
// No I/O, no Spring runtime (we declare a single
// `@ConfigurationProperties` class so operators can override the
// `schema_version` literal if a future major bump happens — but Spring
// itself is consumed `compileOnly` so plugin authors building this in
// isolation don't pull a Spring transitive chain).
//
// # Bytecode 17 / Kotlin 2.0.21
//
// Identical to plugins-spi and interface-engine. Lets a developer on
// JDK 17 build cleanly; the runtime JRE is 21 in Docker and bytecode 17
// is forward-compatible there.

plugins {
    kotlin("jvm") version "2.0.21"
    `java-library`
}

group = "com.bzonfhir.subscriptionservice"
version = "0.1.0-SNAPSHOT"

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

dependencies {
    // The SPI contract this plugin implements. `api` so consumers that
    // depend on this plugin module pick up the SPI types transitively.
    api(project(":plugins-spi"))

    // Kotlin stdlib.
    api("org.jetbrains.kotlin:kotlin-stdlib")

    // Spring is consumed `compileOnly`: the plugin declares a
    // @ConfigurationProperties class, but the actual Spring runtime
    // is supplied by the host (interface-engine). A third party
    // consuming this plugin standalone can either bring Spring
    // themselves or ignore the @ConfigurationProperties annotation.
    compileOnly("org.springframework.boot:spring-boot:3.5.14")
    compileOnly("org.springframework.boot:spring-boot-autoconfigure:3.5.14")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")
}

tasks.withType<org.jetbrains.kotlin.gradle.tasks.KotlinCompile> {
    compilerOptions {
        freeCompilerArgs.add("-Xjsr305=strict")
        jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
    }
}

tasks.withType<Test> {
    useJUnitPlatform()
}
