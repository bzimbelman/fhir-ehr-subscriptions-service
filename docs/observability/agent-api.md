# Agent-queryable observability API

The interface engine exposes a small JSON-only API designed for programmatic consumption by AI agents and on-call automation. Unlike the Prometheus scrape endpoint (machine-friendly raw metrics) or the operator UI (human-friendly dashboards), this surface answers structured questions in a stable, OpenAPI-described shape.

## Endpoints

All endpoints live under `/admin/observe/` on the interface engine's HTTP port (default 8090).

| Endpoint | Question it answers |
|---|---|
| `GET /admin/observe/system` | Is the system healthy? Which feature toggles are on? How big is each queue? |
| `GET /admin/observe/throughput?window=24h` | How many messages flowed through, bucketed by hour or day? |
| `GET /admin/observe/dlq?limit=20` | What's stuck in the dead-letter queue right now? |
| `GET /admin/observe/trace/{correlationId}` | Trace one message across all pipeline rows by correlation id |
| `GET /admin/observe/openapi.json` | The OpenAPI 3.1 spec describing every endpoint |

## Auth

Same `IPF_ADMIN_AUTH_TOKEN` bearer gate as the other `/admin/` endpoints (set in `.env` for Compose, in the Helm `values.yaml` for k8s). When the env var is unset, endpoints are unauthenticated — fine for dev, never for production.

`/admin/observe/openapi.json` is intentionally unauthenticated so an agent can discover the contract before configuring credentials.

## Schema stability

Every response includes `schema_version` (currently `"1.0"`). Stability rules per [`docs/observability/log-schema.md`](log-schema.md):

- **MAJOR** bumps when a REQUIRED field is removed or renamed
- **MINOR** bumps when a new field is added (any tier)
- Field stability tiers (REQUIRED / OPTIONAL / EXPERIMENTAL) are documented in the OpenAPI spec via descriptions and `required` arrays

An agent can pin against a major version and trust the contract for the lifetime of that major.

## Example: how an agent uses this

```text
agent prompt: "is everything okay with the subscription service?"

agent action:
  1. GET /admin/observe/system → parse the queue counts and feature toggles
  2. If queue.dead_letter > 0:
     GET /admin/observe/dlq → fetch the recent DLQ entries
     Summarize the last_error patterns
  3. GET /admin/observe/throughput?window=1h → compare to baseline
  4. Report: "yes / partial / no — here's what's off"

agent prompt: "trace the patient admit for control_id MSGCTRL00042"

agent action:
  1. GET /admin/messages?source_id=MSGCTRL00042 → find the message + correlation_id
  2. GET /admin/observe/trace/<correlation_id> → see all pipeline rows
  3. GET /admin/messages/{id}/effects → see downstream FHIR resources + subscription fires
  4. Report a timeline
```

## Relationship to other surfaces

| Surface | When to use it |
|---|---|
| **This API** (`/admin/observe/*`) | Agents, automation, structured queries with stable schema |
| **Prometheus scrape** (`/actuator/prometheus` on every service) | Time-series, alerting, dashboards |
| **Jaeger / OTel backend** | Per-request distributed traces with span timing |
| **Operator UI** (Epic #398, future) | Humans clicking around to investigate or operate |
| **FHIR API itself** (`/fhir/AuditEvent?...`) | Compliance audit trail of who accessed/modified what |
| **Logs** (JSON to stdout with schema_version) | Free-form text plus structured fields when you have a correlation_id |

The four surfaces overlap intentionally. The agent API is the one you script against. Prometheus is the one you graph. Traces are the one you debug single requests with. Logs are the one you grep when none of the above tells you what you need.

## See also

- [OpenAPI 3.1 spec](https://localhost:8090/admin/observe/openapi.json) — fetch from your running deployment
- [`log-schema.md`](log-schema.md) — schema-stability rules
- [`metric-catalog.md`](metric-catalog.md) — Prometheus metric reference
- [`../admin-api.md`](../admin-api.md) — full `/admin/*` API including `/messages`, `/subscriptions`, and this surface
