# Interface engine admin REST API

Operator-facing REST surface for inspecting, retrying, and purging messages in
the durable inbound store created by Epic #378.

The primary consumer of these endpoints is the **operator UI** (Epic #398,
in `./ui/`) — a Next.js console that proxies every admin call through a
server-side `/api/admin/*` route so the bearer token never reaches the
browser. The UI is shipped as a docker-compose service (port 3000) and as a
Helm chart sibling Deployment (`ui.enabled` toggle); see the [Deployment
targets](../README.md#deployment-targets) section of the root README for
operator-facing setup. The endpoints below remain fully callable via curl
or any other HTTP client — the UI is a convenience layer, not a gate.

The endpoints live on the interface engine's existing HTTP port (default
**8090** — the same port that serves `/actuator/health`) under the `/admin/`
prefix. There is no separate listener, no separate Service, no separate
ingress: anywhere you can reach `/actuator/health`, you can reach `/admin/`.

Ticket history:

- #380 — `ingested_messages` table + JPA layer (the data model exposed below)
- #382 — async worker (consumes the rows the retry endpoint resets to `RECEIVED`)
- **#384** — *this* admin REST API (messages section)
- #383 — retry policy + DLQ transition consumed by the operator workflow below
- **#390** — `/admin/subscriptions/*` endpoints (see the [Subscriptions](#subscriptions-endpoints) section below)

## Auth model

Gated by a single bearer token, configured via the environment variable
`IPF_ADMIN_AUTH_TOKEN`.

| `IPF_ADMIN_AUTH_TOKEN` | Effect on `/admin/**` |
|-|-|
| **unset or empty** | All endpoints are unauthenticated. A `WARN` is logged once at startup. Intended only for ephemeral dev / sandbox use. |
| **set to a value** | Every request must send `Authorization: Bearer <value>`. Missing / mismatched header returns `401 Unauthorized` with `WWW-Authenticate: Bearer`. |

There is no fine-grained scope model: the token grants full read + state-change
power. Treat it like a database password — store it in a secrets manager,
rotate periodically, never commit it to source.

### Wiring

- **Docker Compose**: set `IPF_ADMIN_AUTH_TOKEN` in `deploy/docker/.env` (see
  `deploy/docker/.env.example` for the template).
- **Helm / Kubernetes**: set `interfaceEngine.admin.authToken` in your values
  file. The chart wires that value into the env var on the interface-engine
  Deployment. Default is the empty string (= unauthenticated).

Example (Helm):

```yaml
interfaceEngine:
  admin:
    authToken: "REPLACE_WITH_OPENSSL_RAND_HEX_32"
```

Example (Compose `.env`):

```dotenv
IPF_ADMIN_AUTH_TOKEN=REPLACE_WITH_OPENSSL_RAND_HEX_32
```

## Endpoints

The base URL in all examples below is the interface engine's HTTP base — e.g.
`http://localhost:18090` for Compose, `http://localhost:38090` after a port-forward
of the Helm Service, or `http://subsvc-interface-engine:8090` from inside the
cluster.

### 1. `GET /admin/messages`

List ingested messages, with optional status filter + pagination.

Query parameters (all optional):

| Param | Default | Notes |
|-|-|-|
| `status` | (none — return all) | One of `RECEIVED`, `TRANSFORMING`, `DELIVERED`, `FAILED`, `DEAD_LETTER`. Invalid value returns `400`. |
| `limit` | 50 | Clamped to `[1, 500]`. |
| `offset` | 0 | Negative values coerced to 0. **Note**: rounded down to the nearest multiple of `limit` (page-aligned); the response's `offset` field reflects what was actually used. |

Response (200):

```json
{
  "total": 123,
  "limit": 50,
  "offset": 0,
  "items": [
    {
      "id": 42,
      "received_at": "2026-06-26T13:00:00Z",
      "source_protocol": "HL7V2_MLLP",
      "source_system": "EPIC",
      "source_id": "MSGCTRL00001",
      "message_type": "ADT_A01",
      "status": "DEAD_LETTER",
      "attempt_count": 5,
      "last_attempt_at": "2026-06-26T13:05:00Z",
      "last_error": "matchbox 422: schema violation at PID-3",
      "delivered_at": null
    }
  ]
}
```

`items` does NOT include `raw_message` — that field can be hundreds of KB per
row, and lists are meant for triage. Use the next endpoint for full content.

Example:

```bash
curl -s -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  "http://localhost:18090/admin/messages?status=DEAD_LETTER&limit=20"
```

### 2. `GET /admin/messages/{id}`

Fetch a single message including `raw_message` and `raw_content_type`.

Response (200):

```json
{
  "id": 42,
  "received_at": "2026-06-26T13:00:00Z",
  "source_protocol": "HL7V2_MLLP",
  "source_system": "EPIC",
  "source_id": "MSGCTRL00001",
  "message_type": "ADT_A01",
  "raw_message": "MSH|^~\\&|EPIC|HOSP|RECV|CDS|...|ADT^A01|MSGCTRL00001|P|2.5\r...",
  "raw_content_type": "application/hl7-v2",
  "status": "DEAD_LETTER",
  "attempt_count": 5,
  "last_attempt_at": "2026-06-26T13:05:00Z",
  "next_attempt_at": null,
  "last_error": "matchbox 422: schema violation at PID-3",
  "delivered_at": null
}
```

Errors:

- `404 Not Found` — no row with that id.

Example:

```bash
curl -s -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  http://localhost:18090/admin/messages/42 | jq .
```

### 3. `POST /admin/messages/{id}/retry`

Reset a stuck message so the async worker picks it back up. Sets:

- `status = RECEIVED`
- `attempt_count = 0`
- `next_attempt_at = null`
- `last_error = null`

**Allowed only when current `status` is `FAILED` or `DEAD_LETTER`.** Any other
state returns `409 Conflict` with body:

```json
{
  "error": "invalid_state_for_retry",
  "message": "retry only allowed when status is FAILED or DEAD_LETTER (current: DELIVERED)",
  "id": 42,
  "currentStatus": "DELIVERED"
}
```

Response (200) is the full updated row (same shape as `GET /admin/messages/{id}`).

Errors:

- `404 Not Found` — no row with that id.
- `409 Conflict` — current status not in `{FAILED, DEAD_LETTER}`.

Example:

```bash
curl -s -X POST -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  http://localhost:18090/admin/messages/42/retry | jq .status
```

### 4. `DELETE /admin/messages/{id}`

Permanently remove a message from the inbound store.

**Allowed only when current `status` is `DEAD_LETTER`.** This is deliberately
narrow: deleting a live row (RECEIVED, TRANSFORMING, etc.) would race the
worker; deleting a DELIVERED row destroys audit history.

Response: `204 No Content` on success (no body).

Errors:

- `404 Not Found` — no row with that id.
- `409 Conflict` — current status is not `DEAD_LETTER`.

Example:

```bash
curl -s -X DELETE -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  -o /dev/null -w "%{http_code}\n" \
  http://localhost:18090/admin/messages/42
# 204
```

## State-transition rules — summary

| From status | retry | delete |
|-|-|-|
| `RECEIVED` | 409 | 409 |
| `TRANSFORMING` | 409 | 409 |
| `DELIVERED` | 409 | 409 |
| `FAILED` | **200** | 409 |
| `DEAD_LETTER` | **200** | **204** |

Rationale:

- **retry only from FAILED/DEAD_LETTER** — these are the operator-visible
  failure states. Retrying a `RECEIVED` row would race the worker; retrying
  a `DELIVERED` row would re-send to HAPI and risk double-creation.
- **delete only from DEAD_LETTER** — DLQ rows are by definition not coming
  back through the normal pipeline. Deleting a live row destroys in-flight
  work; deleting a DELIVERED row destroys audit history.

If you need to escape these rules (e.g. emergency surgery on a stuck
TRANSFORMING row), do it with `psql` directly — the admin REST API
deliberately doesn't expose that.

## Retry policy

When the async worker fails to process a row (matchbox transform raised, HAPI
POST raised, etc.), one of two things happens:

1. **Schedule a retry.** The row stays in `FAILED` with `attempt_count` bumped,
   `last_error` recorded, and `next_attempt_at = now() + backoff`. The worker
   picks it back up after that timestamp passes.
2. **Move to DLQ.** Once `attempt_count` reaches `max-attempts`, the row goes
   to `DEAD_LETTER` with `next_attempt_at = NULL`. The worker stops polling it;
   only `POST /admin/messages/{id}/retry` will bring it back.

### Configuration

Five knobs, all configurable via env var (interface-engine) or
`interfaceEngine.worker.retry.*` (Helm):

| Env var | Helm key | Default | Meaning |
|-|-|-|-|
| `IPF_MAX_ATTEMPTS` | `maxAttempts` | `5` | DLQ when `attempt_count` reaches this. |
| `IPF_BACKOFF_BASE_MS` | `backoffBaseMs` | `1000` | Delay after the 1st failure. |
| `IPF_BACKOFF_MAX_MS` | `backoffMaxMs` | `300000` | Hard cap on any computed delay. |
| `IPF_BACKOFF_FACTOR` | `backoffFactor` | `2.0` | Multiplier between consecutive failures. |
| `IPF_DLQ_LOG_LEVEL` | `dlqLogLevel` | `WARN` | Severity of the per-DLQ log line. |

### Backoff formula

```
next_attempt_at = now() + min(BASE * FACTOR^(N-1), MAX) ms
```

…where `N` is the **just-incremented** attempt count. With the default
config (BASE=1s, FACTOR=2.0, MAX=5min, max-attempts=5):

| Failure # | new attempt_count | delay before next try | running total wait |
|-:|-:|-:|-:|
| 1 | 1 | 1s | 1s |
| 2 | 2 | 2s | 3s |
| 3 | 3 | 4s | 7s |
| 4 | 4 | 8s | 15s |
| 5 | 5 | — (DLQ) | 15s wall + DLQ |

A row that's failing consistently spends ~15s in flight before landing in DLQ.
Adjust `BASE` and `FACTOR` together for a different retry budget; `MAX` only
matters once `BASE * FACTOR^(N-1)` exceeds it.

### Log events to monitor

The worker emits two structured log lines that operators / alerts should
key on. Both are single-line `key=value` formats so a `grep` or Loki query
can extract them cleanly:

- `event=retry_scheduled` (INFO) — fires once per scheduled retry. Includes
  `message_id`, `source_system`, `source_id`, `message_type`, `attempt_count`,
  `backoff_ms`, `last_error`. High-volume during an outage; useful for
  diagnosing patterns, not for paging.

- `event=dlq` (default WARN, overridable via `IPF_DLQ_LOG_LEVEL`) — fires
  **once** when a row crosses into DEAD_LETTER. Includes `message_id`,
  `source_system`, `source_id`, `message_type`, `attempt_count`, `last_error`.
  This is the right line to alert on — every DLQ row needs operator
  attention.

Example grep against pod logs:

```bash
kubectl logs -n <ns> deploy/<release>-interface-engine | grep 'event=dlq'
```

### Admin retry resets the policy

`POST /admin/messages/{id}/retry` (see endpoint #3 above) sets:

- `status = RECEIVED`
- `attempt_count = 0`
- `next_attempt_at = null`
- `last_error = null`

A retried message therefore starts the backoff sequence from scratch —
if it fails again, the first delay is `BASE`, not whatever delay the row
last had. This is intentional: an operator retrying a row has implicitly
declared that the upstream cause is fixed; charging the new attempt for
the old failures would punish the operator's diagnosis.

## Pagination conventions

- `limit` defaults to 50, capped at 500. Operators paginating by hand
  shouldn't be able to ask for an unbounded dump that pins the server.
- `offset` is **page-aligned**: the value is rounded down to the nearest
  multiple of `limit` and the response's `offset` field reflects what was
  actually used. So `?limit=50&offset=37` returns the same window as
  `?limit=50&offset=0` and the response says `offset: 0`. Use offsets that
  are multiples of `limit` to avoid surprises.
- Rows are returned **newest first** (by `received_at` desc, tiebroken by
  `id` desc). That's what an operator triaging "what just broke?" wants.
- `total` reflects the filter (e.g. when `status=DEAD_LETTER` is set, it's
  the count of DEAD_LETTER rows, not the table total).

## Operator workflow — "I have a stuck message in DLQ"

Scenario: alerts say there are `DEAD_LETTER` rows. You need to look at them,
fix the upstream issue (a missing reference data row, a malformed map, a
firewall hole), and replay them.

```bash
# 0. Establish a connection. From your laptop against a Helm release in
#    namespace cds-dev, port-forward the cluster Service:
kubectl -n cds-dev port-forward svc/subsvc-interface-engine 38090:8090 &
export ADMIN_URL=http://localhost:38090
export TOKEN="<the value of IPF_ADMIN_AUTH_TOKEN>"

# 1. List dead-letter rows newest first:
curl -s -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/admin/messages?status=DEAD_LETTER" | jq '.items[] | {id, source_id, last_error}'

# 2. Pick one — say id 173 — and pull the full row to inspect raw_message:
curl -s -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/admin/messages/173" | jq .

# 3. Fix the upstream issue (e.g. add the missing reference data row to HAPI,
#    update the StructureMap on matchbox, etc.).

# 4. Replay just that one to confirm the fix:
curl -s -X POST -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/admin/messages/173/retry" | jq .status
# "RECEIVED"

# 5. Watch it flow through. Within a few seconds (worker poll interval) the
#    row should go RECEIVED -> TRANSFORMING -> DELIVERED:
watch -n2 "curl -s -H 'Authorization: Bearer $TOKEN' \
  '$ADMIN_URL/admin/messages/173' | jq -r '.status, .attempt_count, .last_error'"

# 6. If it's now DELIVERED, replay the rest in a loop:
for id in $(curl -s -H "Authorization: Bearer $TOKEN" \
              "$ADMIN_URL/admin/messages?status=DEAD_LETTER&limit=500" | jq -r '.items[].id'); do
  curl -s -X POST -H "Authorization: Bearer $TOKEN" \
    "$ADMIN_URL/admin/messages/$id/retry" >/dev/null
done

# 7. For rows that are genuinely unfixable (truly malformed payloads from a
#    broken sender) and you've confirmed no replay will help, purge them:
curl -s -X DELETE -H "Authorization: Bearer $TOKEN" \
  -o /dev/null -w "%{http_code}\n" \
  "$ADMIN_URL/admin/messages/999"
# 204
```

## Security notes

- The token is a single shared secret. Anyone who has it can read every
  message payload (which may include PHI) and reset/delete arbitrary rows.
- There is no IP allowlist or mTLS enforcement at the application level.
  Rely on cluster NetworkPolicy + Ingress restrictions for transport-level
  segmentation.
- The interceptor uses a constant-time comparison to mitigate token-timing
  attacks. The token is never logged.
- If you suspect the token has leaked: change `IPF_ADMIN_AUTH_TOKEN` and
  re-roll the interface-engine pods. The change takes effect on next start
  (it's read at bean creation, not per-request).

## Subscriptions endpoints

Ticket #390 adds two endpoints under `/admin/subscriptions/` for inspecting
the health of HAPI's per-Subscription delivery state. Same port, same
`/admin/` glob — so the same `IPF_ADMIN_AUTH_TOKEN` bearer-token gate that
covers `/admin/messages/*` covers these too.

These endpoints are **read-only proxies** in front of HAPI's Subscription
store. They do not maintain their own state. Behind the scenes the
interface engine queries HAPI via the same HAPI FHIR client wired by
`FhirConfig`, and reformats the responses into operator-friendly JSON.

### Architectural note

HAPI 7.6 ships the R5 Subscriptions Backport IG (StructureDefinitions and
OperationDefinitions land in the package on startup), but the `$status`
operation **is not wired as a method on the Subscription resource provider
by default** in our build of the HAPI JPA starter image. We verified this
during ticket #390 live testing on Rancher Desktop: requests to
`GET /fhir/Subscription/{id}/$status` come back with HAPI's
`ResourceBinding` "No methods exist for resource: null" warning, not a
useful Parameters payload.

Consequently, the admin endpoints today report `delivery_success_count: 0`,
`delivery_failure_count: 0`, and empty `items[]` for the history endpoint
on every Subscription. The summary still surfaces useful operator data:
which subscriptions exist, whether each is `active`, the endpoint URL, and
whether HAPI flipped any into `status=error` (in which case
`last_attempt_outcome=failure` and `last_error` carries HAPI's text).

A richer per-attempt log would require us to keep our own
`subscription_delivery_log` table fed by the
`SUBSCRIPTION_AFTER_REST_HOOK_DELIVERY` hooks ticket #389 already uses for
metrics. We deferred that — the Prometheus
`hapi_subscription_delivery_total` counter from #389 covers the aggregate
"are deliveries succeeding?" alerting question, and the admin "which
subscriptions are registered? are any in error?" question is answered by
the existing fields. When operators report that they need richer
per-attempt detail than Prometheus + Subscription metadata provides, the
right next step is to either (a) wire HAPI's `$status` operation via a
custom `IResourceProvider` in `hapi-auth`, or (b) implement the own-table
approach. See `HapiSubscriptionStatusClient.kt`'s class-level KDoc.

### 1. `GET /admin/subscriptions/health`

List a summary across all Subscription resources currently registered on
HAPI.

Response (200):

```json
{
  "total": 3,
  "items": [
    {
      "id": "Subscription/123",
      "active": true,
      "channel_type": "rest-hook",
      "endpoint": "https://webhook.example.com/notify",
      "delivery_success_count": 1247,
      "delivery_failure_count": 3,
      "last_attempt_at": "2026-06-26T18:00:00Z",
      "last_attempt_outcome": "success",
      "last_error": null
    }
  ]
}
```

- `total` is the number of subscriptions HAPI returned (capped at 500;
  see `HapiSubscriptionStatusClientImpl.MAX_LIST`).
- `delivery_success_count` / `delivery_failure_count` reflect events
  returned by HAPI's `$status` operation. For subscription types where
  HAPI doesn't track per-attempt events (legacy R4 criteria), both are
  `0` and the aggregate Prometheus counter is the right source of truth.
- `last_attempt_outcome` is `"success"`, `"failure"`, or `null` (no
  attempts recorded). When HAPI's `Subscription.status` is `error`, this
  is forced to `"failure"` even with no events.

Example:

```bash
curl -s -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  "http://localhost:18090/admin/subscriptions/health" | jq .
```

### 1a. New fields on `health` response items (ticket #404)

The ticket #404 operator UI ships two additional fields on each item to
power its detail page and status pill — no breaking change to the existing
contract:

- `status` — raw FHIR R4 `Subscription.status` code:
  `"active"` / `"off"` / `"requested"` / `"error"`. The existing `active`
  boolean stays as a convenience (`active == (status == "active")`).
- `criteria` — the `Subscription.criteria` string (e.g. `"Patient?"`,
  `"SubscriptionTopic?topic=https://example.com/Topic/foo"`). Empty
  string when the resource has no criteria set.

### 2. `GET /admin/subscriptions/{id}/history`

Recent delivery attempts for one Subscription. `{id}` is the bare HAPI
id, e.g. `123`, not the `Subscription/123` reference form.

Query parameters (all optional):

| Param | Default | Notes |
|-|-|-|
| `limit` | 50 | Clamped to `[1, 500]`. |
| `offset` | 0 | Negative values coerced to 0. Rounded down to the nearest multiple of `limit` (page-aligned); the response's `offset` field reflects what was actually used. |

Response (200):

```json
{
  "subscription_id": "Subscription/123",
  "total": 1250,
  "limit": 50,
  "offset": 0,
  "items": [
    {
      "attempted_at": "2026-06-26T18:00:00Z",
      "outcome": "success",
      "http_status": 200,
      "error": null,
      "duration_ms": 142
    }
  ]
}
```

- `items[]` are newest-first.
- `http_status` and `duration_ms` are populated only when the underlying
  HAPI response carries them. For the current `$status` proxy
  implementation both are `null` — they're reserved for the future
  own-table implementation. `outcome` and `error` are always populated.

Errors:

- `404 Not Found` — no Subscription with that id on HAPI.

Example:

```bash
curl -s -H "Authorization: Bearer $IPF_ADMIN_AUTH_TOKEN" \
  "http://localhost:18090/admin/subscriptions/123/history?limit=20" | jq .
```

### 3. `GET /admin/subscriptions/{id}/resource` (ticket #404)

Pretty-printed FHIR R4 `Subscription` resource JSON. Power-feature for
the operator UI's detail panel — operators don't have to leave the
console to inspect the registered configuration.

Response (200): the full `Subscription` resource as JSON, exactly as
HAPI's R4 parser emits it. `resourceType` is always `"Subscription"`.

Errors:

- `404 Not Found` — no Subscription with that id on HAPI.

### 4. `PATCH /admin/subscriptions/{id}/status` (ticket #404)

Flip a Subscription's `status` between `active` and `off` (or any other
FHIR R4 status code) without round-tripping through a FHIR PUT. Used by
the operator UI's one-click toggle.

Request body (`application/json`):

```json
{ "status": "off" }
```

`status` must be one of `active` / `off` / `requested` / `error`.

Response (200) is a `SubscriptionHealthItem` (same shape as the items
in `/health`) reflecting the new state.

Errors:

- `400 Bad Request` — body missing `status`, or value not in the
  R4 vocabulary.
- `404 Not Found` — no Subscription with that id on HAPI.

Audit: emits a single JSON log line at INFO with
`audit_event=subscription_status_changed` so the action surfaces in the
service's log pipeline. Ticket #407 will land a structured
`AuditEvent` resource written to HAPI.

### Operator workflow — "is this subscriber receiving notifications?"

```bash
export ADMIN_URL=http://localhost:18090
export TOKEN="<the value of IPF_ADMIN_AUTH_TOKEN>"

# 1. Quick aggregate scan — anyone with non-zero failure_count?
curl -s -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/admin/subscriptions/health" \
  | jq '.items[] | select(.delivery_failure_count > 0)'

# 2. For a suspect subscription, drill into the recent attempts:
curl -s -H "Authorization: Bearer $TOKEN" \
  "$ADMIN_URL/admin/subscriptions/123/history?limit=20" | jq '.items'

# 3. If the FHIR Subscription itself is in `status=error`, look at
#    the same Subscription resource's `error` field via plain FHIR:
curl -s "$ADMIN_URL/fhir/Subscription/123" | jq '{status, error}'
```

For aggregate alerting use the Prometheus
`hapi_subscription_delivery_total{outcome="failure"}` counter (ticket
#389) — this admin API is for human-driven inspection.

## Log schema and metrics

This admin API is one of two ways agents and operators read state out of
the interface engine. The other is the structured-log and Prometheus
surface, which has its own versioned contract:

- [`observability/log-schema.md`](observability/log-schema.md) — JSON log
  field-stability matrix (every record carries a `correlation_id` that
  ties back to a `/admin/messages/{id}` row).
- [`observability/metric-catalog.md`](observability/metric-catalog.md) —
  Prometheus metrics with stability tiers and label-cardinality rules.

Agents that need per-message state should prefer the admin API. Agents
that need aggregate operational signals should prefer the metrics
endpoint. Both surfaces version independently.
