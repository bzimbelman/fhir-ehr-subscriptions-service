// plugins-builtin/profile-loader — the YAML vendor-profile loader plugin.
//
// Ticket #435 (Epic #425, sets up the bridge to Epic #426 — the vendor
// profile catalog). Reads vendor profile manifest YAML files at boot,
// parses them into typed [ProfileManifest] data classes, validates them
// against a JSON Schema, and registers the parsed profiles in a
// [ProfileRegistry] bean that other parts of the runtime can query.
//
// Why a SEPARATE plugin module:
//
//   * Format swappability. Today the manifest format is YAML; a customer
//     that already has profiles encoded as (say) JSON or a vendor-specific
//     descriptor language can ship a different loader plugin that reads
//     THAT format and produces the same [ProfileRegistry] beans. The rest
//     of the system depends on the registry shape, not the file format.
//
//   * Dependency hygiene. Pulling Jackson-YAML + the JSON Schema validator
//     into one focused module keeps interface-engine's compile classpath
//     unchanged when an operator doesn't deploy any profiles.
//
//   * The same self-demonstrating SPI rationale that drives the other
//     plugins-builtin modules: third parties writing a vendor-profile
//     mapper learn the manifest shape by reading this plugin.
//
// Bytecode 17 + Kotlin 2.0.21 + Spring `compileOnly` — identical to the
// other built-in plugins. The host (interface-engine) brings the Spring
// runtime; we declare a @ConfigurationProperties and a couple of @Bean
// methods on top of it.

plugins {
    kotlin("jvm") version "2.0.21"
    kotlin("plugin.spring") version "2.0.21"
    id("io.spring.dependency-management") version "1.1.7"
    `java-library`
}

group = "com.bzonfhir.subscriptionservice.plugins"
version = "0.1.0-SNAPSHOT"

java {
    sourceCompatibility = JavaVersion.VERSION_17
    targetCompatibility = JavaVersion.VERSION_17
}

repositories {
    mavenCentral()
}

// Match the line interface-engine pins so transitive Jackson and Spring
// versions on the runtime classpath stay aligned.
val springBootVersion = "3.5.14"
val jacksonVersion = "2.18.2"
val jsonSchemaValidatorVersion = "1.5.3"

dependencyManagement {
    imports {
        mavenBom("org.springframework.boot:spring-boot-dependencies:$springBootVersion")
    }
}

dependencies {
    // The SPI we bind profiles to.
    api(project(":plugins-spi"))

    // Kotlin.
    implementation("org.jetbrains.kotlin:kotlin-stdlib")
    implementation("org.jetbrains.kotlin:kotlin-reflect")

    // Jackson YAML reader. Kotlin-module gets us first-class data-class
    // binding (no setter gymnastics). The BOM aligns the YAML reader
    // with the rest of the jackson stack the host brings.
    implementation("com.fasterxml.jackson.dataformat:jackson-dataformat-yaml")
    implementation("com.fasterxml.jackson.module:jackson-module-kotlin")
    implementation("com.fasterxml.jackson.core:jackson-databind")

    // JSON Schema validator. `networknt/json-schema-validator` is the
    // standard JVM library — supports draft 2020-12, no transitive Spring,
    // no transitive Jackson lock-in beyond what we already pull. The Kotlin
    // glue translates our parsed Map<String,Any> manifests into a
    // JsonNode the validator wants.
    implementation("com.networknt:json-schema-validator:$jsonSchemaValidatorVersion")

    // Spring — compileOnly because the host supplies the runtime.
    compileOnly("org.springframework.boot:spring-boot-starter")
    compileOnly("org.springframework.boot:spring-boot-autoconfigure")

    // Tests.
    testImplementation(platform("org.junit:junit-bom:5.10.2"))
    testImplementation("org.junit.jupiter:junit-jupiter")
    testRuntimeOnly("org.junit.platform:junit-platform-launcher")
    testImplementation("org.assertj:assertj-core:3.25.3")
    testImplementation("org.springframework.boot:spring-boot-starter")
    testImplementation("org.springframework.boot:spring-boot-test")
    testImplementation("org.springframework:spring-test")
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
