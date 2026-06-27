# plugins-builtin/hl7v2-mllp

Default HL7 v2 MLLP `IngestSource` implementation for `subscription-service`.

Ticket #431, Epic #425 (built-in plugin refactor — first ingest plugin).

## What it does

Listens on a TCP port for MLLP-framed HL7 v2 messages, parses each one with HAPI, and hands it to the SPI-provided callback as a canonical `PipelineMessage`. The interface-engine wires that callback into its persist pipeline (`IngestPersistService`), so an inbound message ends up as a `RECEIVED` row in `ingested_messages` exactly the way it did before the plugin refactor.

This module is a **behaviour-preserving rewrite** of the legacy `interface-engine/src/main/kotlin/.../routes/IngestRoutes.kt`. Wire protocol, ACK semantics, header extraction, and persist contract are byte-identical to pre-#431. The only thing that changes is the *shape* of the code: the receive path is now an `IngestSource` SPI implementation that a third-party plugin could replace.

## How it plugs in

The module's `Hl7V2MllpAutoConfiguration` is listed in `META-INF/spring/org.springframework.boot.autoconfigure.AutoConfiguration.imports` — Spring Boot 3's plugin-discovery descriptor. Any `@SpringBootApplication` host that has this JAR on its classpath picks up the configuration automatically, with no `@Import` or `@ComponentScan` changes required.

The interface-engine host:

1. Discovers the `Hl7V2MllpIngestSource` bean (and any sibling `IngestSource` beans from other plugins).
2. On `ApplicationReadyEvent`, calls `.start(callback)` on each, where the callback funnels the `PipelineMessage` into `IngestPersistService.persistReceived(...)`.
3. On JVM shutdown (`@PreDestroy`), calls `.stop()` on each to drain in-flight work + close sockets.

See `interface-engine/.../routes/IngestSourceRegistry.kt` for the host-side bridge code.

## Config knobs

All knobs live under the `subscription-service.ingest.hl7v2-mllp` prefix and bind to `Hl7V2MllpProperties`.

| Property                                                 | Env var                                                | Default     | What it does                                                                                |
|----------------------------------------------------------|--------------------------------------------------------|-------------|---------------------------------------------------------------------------------------------|
| `subscription-service.ingest.hl7v2-mllp.enabled`         | `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_ENABLED`       | `true`      | Master enable switch. Off → no listener, no port bound, no callbacks fire.                  |
| `subscription-service.ingest.hl7v2-mllp.port`            | `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_PORT`          | `2575`      | TCP port the listener binds.                                                                |
| `subscription-service.ingest.hl7v2-mllp.host`            | `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_HOST`          | `0.0.0.0`   | Interface to bind. `0.0.0.0` accepts connections from any pod / external IP via NodePort.   |
| `subscription-service.ingest.hl7v2-mllp.character-set`   | `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_CHARACTER_SET` | `UTF-8`     | Charset for decoding wire bytes. ASCII is the spec but UTF-8 is a safe superset.            |

### Legacy property fallback

The pre-#431 code read MLLP port from `subscription-service.mllp.port` (and the env var `MLLP_PORT`). To avoid forcing every existing deployment and ~20 tests to rename their property keys in lock-step, the auto-config also accepts the legacy key when the new one isn't explicitly set. Precedence:

1. New key `subscription-service.ingest.hl7v2-mllp.port` if set to a non-default value.
2. Legacy key `subscription-service.mllp.port` if set.
3. New key's default (2575).

Operators migrating to the new name should just remove the old key once they've switched.

## Troubleshooting

### "Address already in use" at startup

Another process (likely a stale interface-engine instance, or a competing MLLP listener) is bound to the configured port. Either:
- Stop the other process: `lsof -i :2575` then `kill <pid>`.
- Move this plugin to a different port via `SUBSCRIPTION_SERVICE_INGEST_HL7V2_MLLP_PORT=2576`.

### Plugin doesn't start on a host that's supposed to have it

Check:
1. `subscription-service.ingest.hl7v2-mllp.enabled` isn't set to `false`.
2. `org.apache.camel.CamelContext` is on the host's classpath (the auto-config is gated on this — see `@ConditionalOnClass`).
3. The plugin's JAR is on the host's runtime classpath (`./gradlew :interface-engine:dependencies | grep hl7v2-mllp`).
4. Boot logs include `"starting MLLP ingest source on 0.0.0.0:2575 ..."` — if not, auto-config never activated.

### AE ACKs on every message

The route converts any thrown exception inside the persist callback to AE. Most common causes:
- Database unreachable. Check Postgres health, Hikari pool stats.
- The inbound message is missing MSH-3, MSH-9, or MSH-10. Logs include `"receive failed type=... controlId=... stage=persist reason=MSH-X (...) is required"`.

### Wire-level debugging

Use `nc` to send a test message:

```
printf '\x0bMSH|^~\\&|TEST|HOSP|RECEIVER|CDS|20260625120000||ADT^A04|TEST001|P|2.5\rEVN|A04|20260625120000\rPID|1||MRN00001\x1c\r' | nc localhost 2575
```

Expected: an AA ACK containing `MSA|AA|TEST001` and a new row in `ingested_messages` with `source_system=TEST`, `source_id=TEST001`, `message_type=ADT_A04`, `status=RECEIVED`.

## Test suite

| File                          | Scope                                                                                |
|-------------------------------|--------------------------------------------------------------------------------------|
| `Hl7V2MessageParserTest`      | Pure parser: bytes → `PipelineMessage`. 10 tests covering every MSH field mapping.   |
| `Hl7V2MllpIngestSourceTest`   | SPI lifecycle: `meta`, `protocol`, `start`/`stop`, restart cycle. 6 tests.           |
| `Hl7V2MllpEndToEndTest`       | Socket → plugin → callback. 3 tests covering happy path, AE on exception, batching. |

Run with `./gradlew :plugins-builtin:hl7v2-mllp:test`.

The interface-engine module also runs `IngestRoutesTest` end-to-end (real Spring + Testcontainers Postgres) which doubles as the integration proof that the plugin + registry combo behaves identically to the pre-#431 inline route.
