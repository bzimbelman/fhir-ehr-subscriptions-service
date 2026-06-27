// plugins-spi — the public extension surface for subscription-service plugins.
//
// Ticket #430 (Epic #425). This module ships ONLY interfaces + supporting
// value types. No Spring, no HAPI runtime, no IPF — those are consumer
// concerns. A plugin author depending on plugins-spi must bring their own
// FHIR/HAPI/Spring versions; we deliberately keep this surface tiny so
// consumers do not inherit a transitive-version chain from us.
//
// Bytecode level 17 (matches interface-engine):
//   - interface-engine targets 17 so developers on JDK 17 laptops can still
//     build it, and so does this module to stay consistent.
//   - The production runtime is JRE 21 in Docker; bytecode 17 is forward-
//     compatible there. Plugin authors compiling against this SPI on
//     JDK 17 will succeed; authors on JDK 21 can still use modern
//     language features for their OWN bytecode as long as they don't
//     leak >17 bytecode INTO classes that override our interfaces.
//
// The HAPI/FHIR types referenced from the SPI interfaces (FhirResource,
// AuditEventBuilder) are intentionally NOT pulled in as compile deps. We
// declare them via `compileOnly` so the SPI module's classpath stays slim;
// consumers supply their own HAPI version. See README.md for the rationale.

plugins {
    kotlin("jvm") version "2.0.21"
    `java-library`
    `maven-publish`
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

val hapiFhirVersion = "8.10.0"

dependencies {
    // Kotlin standard library — every Kotlin module needs this on the
    // compile/runtime classpath.
    api("org.jetbrains.kotlin:kotlin-stdlib")

    // HAPI FHIR R4 — supplied at compile time only. The SPI references
    // `org.hl7.fhir.r4.model.AuditEvent` etc. for the AuditEventEnricher
    // and StorageBackend surfaces, but we do NOT package HAPI into the
    // plugins-spi JAR. Consumers (interface-engine, hapi/auth, third-party
    // plugin authors) already bring HAPI on their classpath at their own
    // pinned version. Keeping HAPI compileOnly avoids version skew.
    compileOnly("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    compileOnly("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")

    // Tests need HAPI on the classpath because the contract tests
    // instantiate trivial no-op implementations whose method signatures
    // reference HAPI types. Production consumers get HAPI from their own
    // build files; tests get it here so the SPI module is self-testable.
    testImplementation("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    testImplementation("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")
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

publishing {
    publications {
        create<MavenPublication>("maven") {
            from(components["java"])
        }
    }
}
