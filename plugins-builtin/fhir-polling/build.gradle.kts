// plugins-builtin/fhir-polling — built-in FHIR R4 polling IngestSource
// (ticket #434, Epic #425).
//
// A second built-in IngestSource sibling to plugins-builtin/hl7v2-mllp.
// Where the HL7 v2 plugin pulls from a TCP socket, this plugin pulls
// resources from a FHIR R4 server on a configurable schedule and emits
// PipelineMessages downstream. It is the foundation for the Athena
// vendor profile (Epic #426) and any future polling-based source.
//
// Build shape mirrors hl7v2-mllp's:
//
//   1. Kotlin/JVM library module — NOT a Spring Boot executable. The
//      `org.springframework.boot` Gradle plugin is omitted on purpose;
//      its bootJar repackaging would break ServiceLoader discovery of
//      our AutoConfiguration.imports descriptor in downstream apps.
//   2. Depends on plugins-spi only as a public surface (`api`).
//   3. Brings its own HAPI FHIR client deps `implementation`-scoped so
//      the host (interface-engine) doesn't have to drag them in just
//      because some other plugin needs FHIR.
//   4. Spring + Spring Boot are `compileOnly` — the host supplies the
//      runtime versions; the plugin compiles against the API.

plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.spring") version "2.0.21"
    id("io.spring.dependency-management") version "1.1.7"
    `java-library`
}

group = "com.bzonfhir.subscriptionservice.plugins"
version = "0.1.0-SNAPSHOT"

java {
    // Match plugins-spi + interface-engine + hl7v2-mllp. The production
    // runtime is JRE 21 (forward-compatible with 17 bytecode); developer
    // laptops on JDK 17 still build cleanly.
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

// Versions pinned to match interface-engine (which is the only host today).
// Bumping any of these requires re-checking that the host and plugin still
// pin the same line — otherwise we'd ship two HAPI FHIR versions on the
// final classpath and the static singletons would race at startup.
val springBootVersion = "3.5.14"
val hapiFhirVersion = "8.10.0"

dependencyManagement {
    imports {
        mavenBom("org.springframework.boot:spring-boot-dependencies:$springBootVersion")
    }
}

dependencies {
    // The SPI we implement — the only "public API" contract this module
    // depends on. Transitive HAPI/HTTP/Spring deps below are internal
    // implementation details of the plugin.
    api(project(":plugins-spi"))

    // Kotlin.
    implementation("org.jetbrains.kotlin:kotlin-stdlib")
    implementation("org.jetbrains.kotlin:kotlin-reflect")

    // Spring — compileOnly because the host (interface-engine) pins its
    // own Boot version and brings these on the runtime classpath at that
    // exact line. We need them at compile time for @Component,
    // @Configuration, @ConfigurationProperties, @Scheduled, etc.
    compileOnly("org.springframework.boot:spring-boot-starter")
    compileOnly("org.springframework.boot:spring-boot-autoconfigure")
    // @Scheduled lives in spring-context — pulled in transitively via
    // spring-boot but called out here for clarity.
    compileOnly("org.springframework:spring-context")

    // HAPI FHIR R4 — the actual machinery that talks to a FHIR server via
    // IGenericClient. `implementation` because the host shouldn't have to
    // know which FHIR artifacts the polling plugin needs — a sibling
    // plugin that doesn't poll FHIR wouldn't drag any of these in.
    //
    // Note: interface-engine already declares hapi-fhir-base/client/
    // structures-r4 as direct deps (FhirConfig.kt), so when this module
    // is wired into the host the two paths converge on the same version
    // line (8.10.0). The plugin keeps the deps anyway so it remains
    // standalone-buildable / testable.
    implementation("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    implementation("ca.uhn.hapi.fhir:hapi-fhir-client:$hapiFhirVersion")
    implementation("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")
    testImplementation("org.awaitility:awaitility:4.2.2")
    testImplementation("org.mockito.kotlin:mockito-kotlin:5.4.0")

    // Spring on the test classpath so we can drive the auto-config via
    // ApplicationContextRunner (same pattern as audit-event-fhir's tests).
    testImplementation("org.springframework.boot:spring-boot-starter")
    testImplementation("org.springframework.boot:spring-boot-autoconfigure")
    testImplementation("org.springframework.boot:spring-boot-test")
    testImplementation("org.springframework:spring-test")

    // Embedded HTTP-server-based FHIR fixture for the end-to-end test.
    //
    // We use the JDK's built-in `com.sun.net.httpserver.HttpServer`
    // (always on the classpath; no extra dep needed) and hand-craft
    // FHIR+JSON Bundle responses inside the test. The test still
    // exercises the real HAPI IGenericClient -> HTTP -> response
    // parse path — only the SERVER side is a tiny hand-rolled stub,
    // not a real FHIR engine.
    //
    // Tried first: HAPI's RestfulServer + embedded Jetty. The
    // HAPI 8.10 + Jetty 12 + Spring Boot 3.5 BOM combo resolves to
    // incompatible Jetty 11 / 12 mixes on the test classpath. The
    // JDK HttpServer sidesteps the dep tangle entirely.
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
