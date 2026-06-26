# ipf-app

Spring Boot + Apache Camel + IPF + HAPI HL7v2 application. Listens for HL7 v2 over MLLP, parses, and routes by `MSH-9` message type:

- `ADT^A01` → POST to Matchbox `$transform` → parse resulting Bundle → POST as transaction to HAPI → ACK `AA`.
- Everything else → log and ACK `AA` (pass-through; expansion path documented in `IngestRoutes.kt`).
- Any failure in the transform/persist pipeline → ACK `AE` with a reason logged.

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
    ├── main/kotlin/com/bzonfhir/subscriptionservice/ipf/
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

| Var                  | Default                                              | Used for                                                                                                       |
|----------------------|------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| `MLLP_PORT`          | `2575`                                               | MLLP listener bind port.                                                                                       |
| `HAPI_BASE`          | `http://hapi:8080/fhir`                              | HAPI FHIR base URL; target of the transaction POST.                                                            |
| `HAPI_TIMEOUT_MS`    | `30000`                                              | Connect + socket timeout (ms) on the HAPI client.                                                              |
| `MATCHBOX_BASE`      | `http://matchbox:8080/matchboxv3/fhir`               | Matchbox FHIR base URL; target of the `$transform` POST.                                                       |
| `MATCHBOX_TIMEOUT_MS`| `30000`                                              | HTTP connect + response timeout (ms) on the Matchbox call.                                                     |
| `MATCHBOX_SM_ADT_A01`| `http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A01` | Canonical URL of the ADT^A01 → Bundle StructureMap. Override to swap in a project-owned map without rebuilding.|

## Idempotency

The route stamps every transformed Bundle with `Bundle.identifier = {system: urn:ietf:rfc:3986, value: urn:hl7-controlId:<MSH-10>}` before POSTing to HAPI. **HAPI does NOT de-duplicate on this field** — `Bundle.identifier` is metadata about the message, not the contained resources. The marker is currently a tracing aid only.

For true idempotency, the StructureMap should emit conditional creates on the contained resources (e.g., `Patient.request.ifNoneExist=identifier=<MRN system>|<MRN>`). The committed test fixture (`src/test/resources/fixtures/adt-a01-bundle.json`) demonstrates this pattern; the published v2-to-FHIR IG ConceptMaps do not yet describe it.

See `docs/architecture.md` → "Mapping strategy" for the longer-term plan.

## Matchbox + v2-to-FHIR IG dependency state (as of ticket #361)

The route POSTs raw HL7 v2 ER7 to `${MATCHBOX_BASE}/StructureMap/$transform?source=<StructureMap canonical URL>` with `Content-Type: x-application/hl7-v2+er7`. For that to produce a Bundle, Matchbox must have:

1. An executable `StructureMap` resource with the matching canonical URL (e.g. `http://hl7.org/fhir/uv/v2mappings/StructureMap/ADT_A01`).
2. The ability to interpret `x-application/hl7-v2+er7` request bodies.

**Neither is true today against matchbox v3.9.13 + hl7.fhir.uv.v2mappings v1.0.0.** The published v1.0.0 IG ships ConceptMaps, not executable StructureMaps; matchbox v3's CapabilityStatement does not advertise `$transform` either. Until those land, ADT^A01 traffic will get ACK `AE` from the route and the rest of the stack works as designed.

Routes for pass-through message types (everything other than ADT^A01) are unaffected — they ACK `AA` without contacting Matchbox or HAPI.

To complete the end-to-end smoke test described in the ticket: drop a working `StructureMap` JSON under `matchbox/maps/` (or upgrade matchbox once `$transform` over ER7 ships), rerun `fetch-igs.sh`, restart Compose.

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
