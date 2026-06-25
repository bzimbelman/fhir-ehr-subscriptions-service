# ipf-app

Spring Boot + Apache Camel + IPF + HAPI HL7v2 application. Listens for HL7 v2 over MLLP, calls Matchbox to transform messages into FHIR Bundles, posts the bundles to HAPI FHIR.

See [../docs/architecture.md](../docs/architecture.md) "What is IPF?" for background on the stack.

## Layout (planned)

```
ipf-app/
├── build.gradle.kts            ← Kotlin/Gradle build
├── settings.gradle.kts
├── Dockerfile                  ← multi-stage, JRE 21 base
└── src/
    ├── main/kotlin/com/bzonfhir/subscription/
    │   ├── Application.kt
    │   └── routes/IngestRoutes.kt
    ├── main/resources/
    │   └── application.yaml
    └── test/kotlin/...
```
