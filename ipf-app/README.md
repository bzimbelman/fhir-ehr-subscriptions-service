# ipf-app

Spring Boot + Apache Camel + IPF + HAPI HL7v2 application. Listens for HL7 v2 over MLLP, parses, logs, and ACKs (ticket #360). Transform-to-FHIR via Matchbox + POST to HAPI is ticket #361.

See [../docs/architecture.md](../docs/architecture.md) for background on the stack.

## Layout

```
ipf-app/
├── build.gradle.kts            ← Kotlin/Gradle build, pinned versions
├── settings.gradle.kts
├── gradlew, gradle/            ← Gradle 8.11.1 wrapper
├── Dockerfile                  ← multi-stage, JDK 21 build → JRE 21 runtime
├── .dockerignore
├── compose-snippet.yml         ← service block to merge into deploy/docker/docker-compose.yml
├── ca-cert/                    ← optional corporate-MITM root, see below
└── src/
    ├── main/kotlin/com/bzonfhir/subscription/
    │   ├── Application.kt
    │   └── routes/IngestRoutes.kt
    ├── main/resources/
    │   └── application.yaml
    └── test/kotlin/.../IngestRoutesTest.kt
```

## Run locally

```
./gradlew bootRun
# In another shell:
printf '\x0bMSH|^~\\&|EPIC|HOSP|RECEIVER|CDS|20260625120000||ADT^A01|MSGCTRL00001|P|2.5\rEVN|A01|20260625120000\rPID|1||MRN12345^^^HOSP^MR||DOE^JOHN^Q||19800101|M\rPV1|1|I|2000^2012^01\r\x1c\r' \
  | nc -w 3 localhost 2575
```

You should see an `MSA|AA|MSGCTRL00001` ACK and a log line like:

```
Received HL7 v2 message type=ADT^A01 controlId=MSGCTRL00001 sendingApp=EPIC
```

## Test

```
./gradlew test
```

## Docker build

```
docker build -t subscription-service/ipf-app:dev .
docker run --rm -p 8090:8090 -p 2575:2575 subscription-service/ipf-app:dev
```

### Behind a corporate TLS MITM (Netskope/Zscaler/etc.)

Gradle and Maven Central downloads fail with PKIX errors inside the build container if your network intercepts TLS. Drop the corporate CA chain into `ca-cert/cert.pem` before running `docker build` and the Dockerfile will install it into both the OS trust store and the JVM keystore for the build stage only.

```
cp ~/.netskope-ca/full-chain.pem ipf-app/ca-cert/cert.pem
docker build -t subscription-service/ipf-app:dev ipf-app/
```

The `cert.pem` file is gitignored. CI builds outside a corporate proxy don't need this step.

## Environment variables

| Var             | Default                                | Used for                                        |
|-----------------|----------------------------------------|-------------------------------------------------|
| `MLLP_PORT`     | `2575`                                 | MLLP listener bind port.                        |
| `HAPI_BASE`     | `http://hapi:8080/fhir`                | HAPI FHIR base URL (plumbed, not yet wired).    |
| `MATCHBOX_BASE` | `http://matchbox:8080/matchboxv3/fhir` | Matchbox FHIR base URL (plumbed, not yet wired).|

## Pinned versions

| Component        | Version  | Source / rationale                                       |
|------------------|----------|----------------------------------------------------------|
| Kotlin           | 2.0.21   | Last release before 2.2.x; aligned with Spring Boot 3.5. |
| Spring Boot      | 3.5.14   | Matches IPF 5.2.0's dependencies pom.                    |
| Apache Camel     | 4.18.2   | Matches IPF 5.2.0's dependencies pom.                    |
| IPF              | 5.2.0    | Latest 5.x as of 2026-06-08.                             |
| HAPI HL7v2       | 2.6.0    | Matches IPF 5.2.0's dependencies pom.                    |
| HAPI FHIR        | 8.10.0   | Matches IPF 5.2.0's dependencies pom.                    |
| Gradle           | 8.11.1   | Wrapper + Docker build image.                            |
| JDK (build)      | 21       | `gradle:8.11.1-jdk21`.                                   |
| JRE (runtime)    | 21       | `eclipse-temurin:21-jre-jammy`.                          |
