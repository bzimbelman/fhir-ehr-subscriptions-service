# Interface engine admin REST API

Operator-facing REST surface for inspecting, retrying, and purging messages in
the durable inbound store created by Epic #378.

The endpoints live on the interface engine's existing HTTP port (default
**8090** — the same port that serves `/actuator/health`) under the `/admin/`
prefix. There is no separate listener, no separate Service, no separate
ingress: anywhere you can reach `/actuator/health`, you can reach `/admin/`.

Ticket history:

- #380 — `ingested_messages` table + JPA layer (the data model exposed below)
- #382 — async worker (consumes the rows the retry endpoint resets to `RECEIVED`)
- **#384** — *this* admin REST API
- #383 — retry policy + DLQ transition consumed by the operator workflow below

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
