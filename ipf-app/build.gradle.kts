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

dependencyManagement {
    imports {
        mavenBom("org.apache.camel.springboot:camel-spring-boot-bom:$camelVersion")
    }
}

dependencies {
    // Spring Boot core + actuator for /actuator/health.
    implementation("org.springframework.boot:spring-boot-starter")
    implementation("org.springframework.boot:spring-boot-starter-web")
    implementation("org.springframework.boot:spring-boot-starter-actuator")

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

    // Tests.
    testImplementation("org.springframework.boot:spring-boot-starter-test")
    testImplementation("org.apache.camel:camel-test-spring-junit5:$camelVersion")
    testImplementation("org.apache.camel:camel-test-junit5:$camelVersion")
    testImplementation("org.awaitility:awaitility:4.2.2")
    testImplementation("org.mockito.kotlin:mockito-kotlin:5.4.0")
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
