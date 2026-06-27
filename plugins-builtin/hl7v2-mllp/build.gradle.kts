// plugins-builtin/hl7v2-mllp — the default HL7 v2 MLLP IngestSource (ticket #431).
//
// Epic #425's plugin-host refactor moves what was once inline Camel-MLLP code
// inside the interface-engine module into a self-contained "plugin" module
// that:
//
//   1. Compiles against the plugins-spi `IngestSource` interface.
//   2. Brings its OWN Camel-MLLP + HAPI HL7 v2 deps — interface-engine no
//      longer needs to pull them transitively if a deployment swaps in a
//      different ingest plugin (e.g. fhir-r4-polling).
//   3. Ships a Spring Boot auto-config (under
//      `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports`)
//      so a `@SpringBootApplication` host picks the plugin up automatically
//      when the JAR is on the classpath — no extra `@Import` or
//      `@ComponentScan` tweaks.
//
// We avoid the full `org.springframework.boot` Gradle plugin here on
// purpose: this is a LIBRARY module, not an executable. The Boot plugin's
// bootJar repackaging is wrong for libraries (it produces an inflate-able
// fat JAR that breaks ServiceLoader and auto-config discovery in
// downstream apps). We pull in spring-boot-dependencies as a BOM via
// `io.spring.dependency-management` to get version alignment with the
// interface-engine host, and stop there.

plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.spring") version "2.0.21"
    id("io.spring.dependency-management") version "1.1.7"
    `java-library`
}

group = "com.bzonfhir.subscriptionservice.plugins"
version = "0.1.0-SNAPSHOT"

java {
    // Bytecode level 17 matches interface-engine + plugins-spi. Plugin
    // authors building against this module on JDK 17 succeed; the
    // production runtime is JRE 21 (forward-compatible).
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

// Versions pinned to match what interface-engine uses today (which in turn
// match IPF 5.2.0's dependencies pom). Bumping any of these requires
// re-checking that the interface-engine and the plugin pin the same
// transitive line — otherwise we'd have two Camel-MLLP versions on the
// final classpath, which Camel doesn't like.
val springBootVersion = "3.5.14"
val camelVersion = "4.18.2"
val hapiHl7v2Version = "2.6.0"
val ipfVersion = "5.2.0"

dependencyManagement {
    imports {
        mavenBom("org.springframework.boot:spring-boot-dependencies:$springBootVersion")
        mavenBom("org.apache.camel.springboot:camel-spring-boot-bom:$camelVersion")
    }
}

dependencies {
    // The SPI we implement — the only "public API" contract this module
    // depends on. Transitive Spring/Camel/HAPI deps below are internal
    // implementation details of the plugin.
    api(project(":plugins-spi"))

    // Kotlin.
    implementation("org.jetbrains.kotlin:kotlin-stdlib")
    implementation("org.jetbrains.kotlin:kotlin-reflect")

    // Spring — `compileOnly` because the host (interface-engine) brings
    // these on the runtime classpath at the exact version it pins. We
    // need them at compile time for `@Component`, `@Configuration`,
    // `@ConfigurationProperties`, etc. Avoiding `implementation` here
    // keeps the plugin JAR from re-exposing Spring as a transitive
    // dependency to its own consumers (which would be weird — plugins
    // are loaded INTO a Spring host, not consumed FROM elsewhere).
    compileOnly("org.springframework.boot:spring-boot-starter")
    compileOnly("org.springframework.boot:spring-boot-autoconfigure")

    // Camel + IPF — the actual machinery that listens on the MLLP socket
    // and parses HL7 v2 wire bytes into HAPI Message objects. These ARE
    // implementation deps because the host shouldn't have to know which
    // Camel-component / IPF artifacts a given ingest plugin needs. Adding
    // a sibling plugin (fhir-r4-polling) wouldn't drag any of these in.
    implementation("org.apache.camel.springboot:camel-mllp-starter")
    implementation("org.apache.camel.springboot:camel-hl7-starter")
    // ipf-platform-camel-hl7 gives us the `.unmarshal().hl7()` DSL on
    // RouteBuilder — without it, the route can't parse the wire bytes
    // into a HAPI Message.
    implementation("org.openehealth.ipf.platform-camel:ipf-platform-camel-hl7:$ipfVersion")

    // HAPI HL7 v2 — the parser library Camel's `.hl7()` DSL uses, plus
    // the v2.5 structures package (covers ADT^A04 and the rest of the
    // message families we receive today).
    implementation("ca.uhn.hapi:hapi-base:$hapiHl7v2Version")
    implementation("ca.uhn.hapi:hapi-structures-v25:$hapiHl7v2Version")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")
    testImplementation("org.awaitility:awaitility:4.2.2")
    testImplementation("org.mockito.kotlin:mockito-kotlin:5.4.0")

    // For the end-to-end socket test we need to run a Camel context that
    // contains our route + the MLLP component, but without Spring at
    // all. `camel-test-junit5` gives us the bare-bones harness.
    testImplementation("org.apache.camel:camel-test-junit5:$camelVersion")
    // The plain Camel-MLLP + HL7 components (no `-starter` Spring Boot
    // glue) so the test harness can register them on the CamelContext
    // without Spring Boot autoconfig running.
    testImplementation("org.apache.camel:camel-mllp:$camelVersion")
    testImplementation("org.apache.camel:camel-hl7:$camelVersion")
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
