// plugins-builtin/audit-event-fhir — the first built-in plugin.
//
// Ticket #432 (Epic #425). Re-expresses the in-tree HAPI
// `AuditEventInterceptor` as a built-in plugin that implements the
// `AuditEventEnricher` SPI from `plugins-spi/`.
//
// Why a separate module:
//   - It exercises the SPI as a real consumer would: a plugin author
//     pulls in plugins-spi + HAPI R4, writes their enricher, and is done.
//     Keeping the built-in version on the same shape keeps the contract
//     honest.
//   - The hapi/auth Maven module continues to own the interceptor's wiring
//     into HAPI (`@Hook` on `Pointcut.SERVER_OUTGOING_RESPONSE`). This
//     plugin is the part that builds the FHIR AuditEvent — the part a
//     third party could swap in.
//
// Bytecode 17 — matches plugins-spi and interface-engine. The HAPI image
// runs JRE 21 so 17 bytecode is forward-compatible.
//
// Maven interop (see README.md): the plan is to consume this JAR from
// `hapi/auth/pom.xml` via `mvn install` of the published artifact into
// `~/.m2`. The `publishToMavenLocal` task is wired up by the
// `maven-publish` plugin below.

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
val springBootVersion = "3.2.6"

dependencies {
    // Kotlin standard library — every Kotlin module needs this on the
    // compile/runtime classpath.
    api("org.jetbrains.kotlin:kotlin-stdlib")

    // The SPI surface this plugin implements. `api` because we expose
    // `AuditEventEnricher` types on our public method signatures.
    api(project(":plugins-spi"))

    // HAPI FHIR R4 — needed at compile time to build the AuditEvent
    // resource. `compileOnly` so consumers supply their own HAPI version
    // (the same rationale as plugins-spi). The hapi/auth runtime brings
    // HAPI 7.6.0; the SPI itself compiles against 8.10.0. The AuditEvent
    // model is binary-compatible between the two on the slots we touch.
    compileOnly("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    compileOnly("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")

    // Spring Boot autoconfig — `compileOnly` because consumers (HAPI image
    // here, future Gradle-based hapi/auth in a follow-up) bring their own
    // Spring Boot. We don't want the plugin to pin a Boot version.
    compileOnly("org.springframework.boot:spring-boot-autoconfigure:$springBootVersion")
    compileOnly("org.springframework.boot:spring-boot:$springBootVersion")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")

    // Tests need real HAPI on the classpath — they build AuditEvent
    // resources and assert on the FHIR shape.
    testImplementation("ca.uhn.hapi.fhir:hapi-fhir-base:$hapiFhirVersion")
    testImplementation("ca.uhn.hapi.fhir:hapi-fhir-structures-r4:$hapiFhirVersion")
    testImplementation("org.springframework.boot:spring-boot-autoconfigure:$springBootVersion")
    testImplementation("org.springframework.boot:spring-boot:$springBootVersion")

    // ApplicationContextRunner — the recommended way to test
    // Spring Boot autoconfig without a full @SpringBootTest. Same pattern
    // used by hapi/auth's AuthAutoConfigurationTest.
    testImplementation("org.springframework.boot:spring-boot-test:$springBootVersion")
    testImplementation("org.springframework:spring-test:6.1.14")
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
