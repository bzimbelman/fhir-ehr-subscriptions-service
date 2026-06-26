# External Subscribers — Integration Guide

> Audience: developers at an external system who have been told "you can
> register a Subscription with our FHIR server to get notifications about
> Patient changes." This guide walks you from zero to a working
> subscription, end-to-end. You do not need to have read any other doc in
> this repo first.

## 1. Overview

The **subscription-service** is a FHIR R4 server that turns HL7 v2 feeds
from clinical systems (EHRs, labs, radiology, etc.) into FHIR resources
and lets external systems register **Subscriptions** to be notified when
those resources change. Operators (the people running the service)
ingest HL7 v2; subscribers (you) read FHIR and receive notifications.
The server is HAPI FHIR 7.x speaking FHIR R4, fronted by an OAuth2/OIDC
identity provider (Keycloak in the reference deployment, any OIDC
provider in general — see [`auth.md`](auth.md)).

**Subscribing** in FHIR terms means creating a `Subscription` resource on
the server that describes (a) what you care about — a *criteria* such as
"any Patient is created or updated" — and (b) where the server should
deliver notifications when something matching happens — a *channel*
(your HTTPS endpoint, a WebSocket you'll connect, an email address, or
a FHIR Messaging endpoint). The server stores the Subscription, watches
for matching resources, and pushes notifications to your channel as
they occur. The Subscription's `status` transitions to `active` once the
server has accepted and started monitoring it.

Two **subscription models** are supported on this server, both speaking
FHIR R4 over the same `/fhir/Subscription` endpoint:

- **Legacy R4 criteria-based** — `Subscription.criteria` is a FHIR
  search expression (e.g. `Patient?` or `Observation?code=8867-4`). The
  server fires the channel whenever a resource matching that search is
  created or updated. Simple to write, well understood, supported by
  every R4-aware FHIR server. **Use this unless you have a reason not
  to.**
- **R5 Topic-based via the Subscriptions Backport IG** —
  `Subscription.criteria` is a *canonical URL* pointing at a
  `SubscriptionTopic` resource that defines the event boundary
  (e.g. "Patient created" vs "Patient updated"), and a filter goes in an
  extension. This is the forward-shaped model R5 ships natively. Use
  this if your stack is already aligned with the Backport IG, or if
  your operator has published Topics that match your needs better than
  raw criteria.

Either model produces the same channel notifications. The choice is
about how you describe *what you want* — not how delivery works.

## 2. Prerequisites

Before you can register a Subscription you need three things from your
operator:

1. **OAuth2/OIDC credentials.** The operator provisions a confidential
   client for your system in their identity provider and gives you the
   following out-of-band (encrypted email, password manager share —
   never in a chat or ticket comment):
   - `client_id` (e.g. `subscription-service-acme`)
   - `client_secret`
   - **Issuer URL** — the `.well-known/openid-configuration` lives at
     `${issuer}/.well-known/openid-configuration`
   - **Token endpoint** — usually
     `${issuer}/protocol/openid-connect/token` for a Keycloak-issued
     credential
   - The set of **SMART scopes** granted to your client (typically
     `system/Subscription.crus` plus the read scope on whatever
     resource(s) you care about, e.g. `system/Patient.r`). The full
     scope catalog is documented in [`auth.md`](auth.md).
2. **A callback the server can reach.** Concretely, one of:
   - An **HTTPS endpoint** under your control that can accept inbound
     `POST` / `PUT` from the server (`rest-hook` channel — most common).
   - A **WebSocket client** willing to hold a connection open to
     `wss://<your-subscription-service>/websocket` (`websocket`
     channel).
   - A **mailbox** that can receive notification emails (`email`
     channel — supported by HAPI upstream but disabled in this stack
     today; see [§6](#6-channel-options)).
   - A **FHIR Messaging endpoint** (`message` channel) for B2B
     integration patterns.
3. **The base FHIR URL** for the subscription-service instance, e.g.
   `https://subscription-service.example.com/fhir`. All examples below
   assume that base.

Throughout this guide we use placeholder hostnames
(`your-keycloak.example.com`, `subscription-service.example.com`) and a
fake webhook (`my-callback.example.com`). Substitute your own. The
maintainer's reference deployment is named in the callout at the bottom
of this page.

## 3. Step-by-step: register a Subscription (legacy R4 criteria-based)

This is the path most subscribers will use today.

### 3.1 Obtain a token

The server validates `Authorization: Bearer <JWT>` on every request
(unless your operator has deliberately disabled auth for a sandbox).
Tokens come from your identity provider via the OAuth2
`client_credentials` grant:

```bash
TOKEN=$(curl -fsS -X POST \
  https://your-keycloak.example.com/realms/<realm>/protocol/openid-connect/token \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  -d "grant_type=client_credentials" \
  -d "client_id=<your-client-id>" \
  -d "client_secret=<your-client-secret>" \
  -d "scope=system/Subscription.crus system/Patient.r" \
  | jq -r .access_token)

# Sanity check — the FHIR CapabilityStatement is anonymous-accessible
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  https://subscription-service.example.com/fhir/metadata \
  | jq '{fhirVersion, software}'
```

Tokens are short-lived (15 minutes in the reference deployment).
Re-request on demand rather than refreshing — `client_credentials`
clients in this stack do not issue refresh tokens.

### 3.2 POST the Subscription

```bash
curl -fsS -X POST \
  https://subscription-service.example.com/fhir/Subscription \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/fhir+json" \
  -d @- <<'EOF'
{
  "resourceType": "Subscription",
  "status": "requested",
  "reason": "Notify me when any Patient is created or updated",
  "criteria": "Patient?",
  "channel": {
    "type": "rest-hook",
    "endpoint": "https://my-callback.example.com/notify",
    "payload": "application/fhir+json",
    "header": ["Authorization: Bearer <my-callback-secret>"]
  }
}
EOF
```

A successful response is `201 Created` with the Subscription resource
in the body, including a server-assigned `id`. The initial `status`
is `requested`; the server activates the subscription asynchronously
(usually within a second) and the `status` transitions to `active`.
Verify:

```bash
SUB_ID=<id-from-response>
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  https://subscription-service.example.com/fhir/Subscription/${SUB_ID} \
  | jq '{id, status, criteria, endpoint: .channel.endpoint}'
```

### 3.3 Field-by-field

- **`status`** — Start with `"requested"`. The server transitions you
  to `"active"` once monitoring begins. Set to `"off"` later to pause
  delivery without deleting the Subscription.
- **`reason`** — A free-text description, for your own records and for
  ops debugging. Required by FHIR.
- **`criteria`** — A FHIR search expression. The server fires the
  channel whenever a resource matching this search is created or
  updated. Examples:
  - `Patient?` — any Patient, any change
  - `Patient?identifier=urn:oid:1.2.3.4|*` — only Patients with an
    identifier in the given system
  - `Observation?code=http://loinc.org|8867-4` — only Observations with
    LOINC code 8867-4 (heart rate)
  - `Encounter?status=in-progress` — only Encounters whose status is
    in-progress
- **`channel.type`** — One of `rest-hook`, `websocket`, `email`,
  `message`. See [§6](#6-channel-options).
- **`channel.endpoint`** — Where the server delivers notifications.
  For `rest-hook`: an absolute HTTPS URL. For `websocket`: not used
  on the Subscription (you connect to the server's WebSocket
  endpoint).
- **`channel.payload`** — The MIME type the server should send. Common
  values: `application/fhir+json` (full resource body), `application/fhir+xml`,
  or *omitted* (id-only notification — no body, just the URL containing
  the resource ID). See [§5](#5-notification-payload-format).
- **`channel.header`** — An array of HTTP headers the server attaches
  verbatim to every outbound notification. **Use this for callback
  auth** — register a bearer secret here and verify it on each
  incoming notification.

## 4. Step-by-step: register a Subscription (R5 Topic-based via Backport IG)

The Subscriptions Backport IG brings R5's Topic-based model onto R4.
Instead of `criteria` being a search expression, it's a *canonical URL*
that points at a `SubscriptionTopic` resource. The Topic defines the
event boundary; a separate extension carries any narrowing filter.

### 4.1 Browse available SubscriptionTopics

```bash
curl -fsS -H "Authorization: Bearer ${TOKEN}" \
  https://subscription-service.example.com/fhir/SubscriptionTopic \
  | jq '.total, .entry[]?.resource | {url, title, status}'
```

A fresh deployment may return `total=0` — the Backport IG ships
`StructureDefinition`s but not example `SubscriptionTopic` instances.
Topics are published by your operator. Ask them which Topics are
available; if none are, use the legacy criteria-based path from
[§3](#3-step-by-step-register-a-subscription-legacy-r4-criteria-based).

### 4.2 POST a Subscription referencing a Topic

```bash
curl -fsS -X POST \
  https://subscription-service.example.com/fhir/Subscription \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/fhir+json" \
  -d @- <<'EOF'
{
  "resourceType": "Subscription",
  "meta": {
    "profile": [
      "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-subscription"
    ]
  },
  "status": "requested",
  "reason": "Notify me on Patient create events for our org",
  "criteria": "http://example.org/SubscriptionTopic/patient-create",
  "_criteria": {
    "extension": [
      {
        "url": "http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-filter-criteria",
        "valueString": "Patient?managingOrganization=Organization/acme"
      }
    ]
  },
  "channel": {
    "type": "rest-hook",
    "endpoint": "https://my-callback.example.com/notify",
    "payload": "application/fhir+json",
    "header": ["Authorization: Bearer <my-callback-secret>"]
  }
}
EOF
```

Two Backport-specific differences from [§3](#3-step-by-step-register-a-subscription-legacy-r4-criteria-based):

- **`meta.profile`** — declares the resource conforms to the
  `backport-subscription` profile. Some servers reject Backport-shaped
  Subscriptions without this; HAPI is lenient but you should set it.
- **`criteria` + `_criteria.extension`** — `criteria` carries the
  Topic canonical URL, and the
  `backport-filter-criteria` extension on the criteria element carries
  the narrowing search expression. The server's Topic definition
  determines what filters are allowed.

Channel types, payload formats, and headers behave identically across
both models.

## 5. Notification payload format

What your endpoint actually receives depends on `channel.payload`:

### 5.1 With `payload: "application/fhir+json"` (full resource)

The server sends an HTTP `PUT` to `${endpoint}/{ResourceType}/{id}`
with the resource body in the request:

```
PUT https://my-callback.example.com/notify/Patient/506
Authorization: Bearer <my-callback-secret>
Content-Type: application/fhir+json; charset=UTF-8

{
  "resourceType": "Patient",
  "id": "506",
  "meta": {
    "versionId": "1",
    "lastUpdated": "2026-06-26T14:28:48.347+00:00"
  },
  "name": [{"family": "WebhookTest", "given": ["Sub365"]}]
}
```

Most subscribers want this shape. The path tells you which resource
changed; the body gives you its current state.

### 5.2 With `payload` omitted (id-only)

The server sends an HTTP `POST` to `${endpoint}` with an **empty
body**. The path itself is the notification — you know *that* something
matched, but you must `GET` the resource yourself to learn what. Use
this when your subscribers prefer to pull fresh state regardless (e.g.
for cache-consistency or for compliance reasons that forbid receiving
PHI over the channel).

```
POST https://my-callback.example.com/notify
Authorization: Bearer <my-callback-secret>
Content-Length: 0
```

### 5.3 With `payload: "application/fhir+json"` and the Backport IG

Backport-IG subscribers get a `notification-event` Bundle wrapping a
`SubscriptionStatus` plus the changed resource:

```json
{
  "resourceType": "Bundle",
  "type": "history",
  "timestamp": "2026-06-26T14:28:48.347+00:00",
  "entry": [
    {
      "fullUrl": "urn:uuid:...",
      "resource": {
        "resourceType": "SubscriptionStatus",
        "status": "active",
        "type": "event-notification",
        "subscription": {"reference": "Subscription/505"},
        "topic": "http://example.org/SubscriptionTopic/patient-create",
        "notificationEvent": [
          {"eventNumber": "12", "focus": {"reference": "Patient/506"}}
        ]
      }
    },
    {
      "fullUrl": "https://subscription-service.example.com/fhir/Patient/506",
      "resource": { "resourceType": "Patient", "id": "506", "...": "..." }
    }
  ]
}
```

The `eventNumber` is monotonically increasing per subscription — store
it on your side to detect gaps (see delivery semantics below).

### 5.4 Delivery semantics

These hold regardless of channel type or payload format:

- **At-least-once delivery.** Notifications may be retried on
  transient failures. Your endpoint MUST be idempotent — receiving the
  same notification twice should be a no-op.
- **Order is NOT guaranteed across resources.** Two updates to two
  different Patients can arrive in either order. Within a single
  resource, you can detect stale notifications via `meta.versionId` —
  always trust the highest `versionId` you've seen for a given `id`.
- **Notifications carry the resource ID, not necessarily complete
  state.** Even with `payload: "application/fhir+json"`, treat the
  notification as a *signal to refresh*. `GET` the resource if you need
  a guaranteed-current view; the notification body is a point-in-time
  snapshot that can be racing against subsequent updates.
- **Retries are server-controlled.** The server retries failed
  deliveries with exponential backoff. Exact attempt count and backoff
  intervals are operator-configurable and may be tuned by future
  hardening tickets; assume "several attempts over a few minutes" and
  design your endpoint to be available — not for the server to retry
  indefinitely.

## 6. Channel options

### 6.1 `rest-hook` (most common)

The server `POST`s (or `PUT`s, see [§5](#5-notification-payload-format))
each notification to `channel.endpoint`.

```jsonc
{
  "channel": {
    "type": "rest-hook",
    "endpoint": "https://my-callback.example.com/notify",
    "payload": "application/fhir+json",       // optional; omit for id-only
    "header": ["Authorization: Bearer <my-callback-secret>"]
  }
}
```

**Receiving side**: A standard HTTPS server. Return `2xx` to ack the
notification — anything else (or a timeout) is treated as a delivery
failure and triggers retry.

**Failure handling**: Return `5xx` for transient failures (the server
will retry); return `4xx` (`410 Gone`, `404 Not Found`) if the endpoint
is permanently retired and you want the server to stop delivering. Note
that the server does **not** automatically mark the Subscription as
errored on a `4xx`; it just retries less aggressively. To actually
suspend delivery from your side, the server-side operator must change
the Subscription's `status` to `off`.

### 6.2 `websocket`

The server pushes notifications over a WebSocket you connect to. There
is no callback URL — your client opens the connection and keeps it
alive.

```jsonc
{
  "channel": {
    "type": "websocket",
    "payload": "application/fhir+json"
  }
}
```

**Receiving side**: After creating the Subscription, open
`wss://subscription-service.example.com/websocket`. Send the message
`bind <subscription-id>` to bind that connection to a specific
Subscription. The server replies `bound <subscription-id>` and then
streams `ping <subscription-id>` messages each time the Subscription
fires. With `payload: "application/fhir+json"`, the server follows the
ping with the resource body as a separate message.

**Failure handling**: If the connection drops the server cannot deliver
— there is no replay buffer. Reconnect promptly, then `GET` resources
that may have changed during the outage. WebSocket subscriptions are
best for "I want low-latency notifications when I'm online; I'll
reconcile state on reconnect."

### 6.3 `email`

Upstream HAPI supports email channels but **this stack disables email
delivery** (no SMTP relay is configured). Attempting to register an
email Subscription succeeds, but the channel never fires. Use
`rest-hook` instead. If your use case genuinely needs email, file an
ops ticket — provisioning an outbound SMTP relay is an operator task,
not a subscriber one.

### 6.4 `message`

For B2B integrations that already speak FHIR Messaging. The server
posts a FHIR `Bundle` of `type=message` to your endpoint when the
Subscription fires.

```jsonc
{
  "channel": {
    "type": "message",
    "endpoint": "https://my-callback.example.com/fhir/$process-message",
    "header": ["Authorization: Bearer <my-callback-secret>"]
  }
}
```

This is rarely the right choice for a brand-new integration —
`rest-hook` is simpler and covers the same ground. Use this if you
already have a FHIR Messaging endpoint that handles `MessageHeader` +
focal resource for other inbound flows.

## 7. Security requirements

The server enforces a **channel security policy** documented in detail
in [`architecture.md`](architecture.md) (the "Subscription channel
security" section). The policy has three modes (`strict` / `relaxed` /
`permissive`); operators pick the mode at deployment time. From your
side, build to the **`strict` defaults** and you will work everywhere:

- **`channel.endpoint` MUST be HTTPS.** HTTP endpoints are rejected
  with `422 Unprocessable Entity` in `strict` mode and `relaxed` mode.
  Only `permissive` (sandbox / dev) accepts HTTP, and even then you
  should never use it for real PHI.
- **`channel.header` MUST contain an `Authorization` header.**
  Generate a long random secret on your side, register it as
  `["Authorization: Bearer <secret>"]` on the Subscription, and have
  your callback verify it on every incoming notification. The server
  forwards the header verbatim — it does not generate a token for you.
- **Verify the bearer on every notification.** Do NOT trust the source
  IP. The server's outbound IP can change, and any caller on the
  public internet can attempt a `POST` to your endpoint. The
  registered bearer is the *only* thing that authenticates a
  notification as coming from this server.
- **WebSocket subscriptions inherit your access-token's identity.** A
  WebSocket subscriber must authenticate the WS connection with a
  bearer token (in the `Sec-WebSocket-Protocol` header, conventionally
  `bearer-${access-token}`). The server scopes notifications to the
  authenticated client.

### 7.1 Worked example: subscriber-side validation

Node.js (Express):

```js
const SUB_SECRET = process.env.SUB_SECRET;  // the bearer you registered

app.use('/notify', (req, res, next) => {
  const got = req.header('authorization') || '';
  if (got !== `Bearer ${SUB_SECRET}`) {
    return res.status(401).end();
  }
  next();
});

app.post('/notify', (req, res) => {
  // id-only notification — POST with empty body
  res.status(200).end();
  // fetch the resource asynchronously...
});

app.put('/notify/:resourceType/:id', (req, res) => {
  // full-resource notification — PUT with the resource body
  enqueueForProcessing(req.body);
  res.status(200).end();
});
```

Python (Flask):

```python
import os, hmac
from flask import Flask, request, abort

app = Flask(__name__)
SUB_SECRET = os.environ['SUB_SECRET']
EXPECTED = f'Bearer {SUB_SECRET}'

@app.before_request
def verify_bearer():
    got = request.headers.get('Authorization', '')
    if not hmac.compare_digest(got, EXPECTED):
        abort(401)

@app.route('/notify', methods=['POST'])
def id_only():
    return '', 200

@app.route('/notify/<resource_type>/<resource_id>', methods=['PUT'])
def full_resource(resource_type, resource_id):
    # request.get_json() has the body
    return '', 200
```

Both examples use a **constant-time** string comparison
(`hmac.compare_digest`) to defeat timing-attack token discovery. Don't
use `==` on a secret.

## 8. Troubleshooting

### "I'm not receiving notifications"

In order:

1. **Confirm the Subscription is `active`.**
   ```bash
   curl -fsS -H "Authorization: Bearer ${TOKEN}" \
     https://subscription-service.example.com/fhir/Subscription/${SUB_ID} \
     | jq '{status, error: .error}'
   ```
   If `status=requested` for more than 30 seconds, the server failed
   to activate the subscription — `Subscription.error` may have a
   description. If `status=error`, the channel rejected one or more
   recent deliveries. If `status=off`, the operator has paused it.
2. **Confirm your criteria matches.** Run the equivalent FHIR search:
   ```bash
   curl -fsS -H "Authorization: Bearer ${TOKEN}" \
     "https://subscription-service.example.com/fhir/Patient?" \
     | jq '.total'
   ```
   `total=0` means no resources match — the Subscription has nothing
   to fire on yet. Create one and try again.
3. **Confirm your endpoint is reachable from the server.** From a
   machine outside your network:
   ```bash
   curl -i -X POST https://my-callback.example.com/notify \
     -H "Authorization: Bearer <my-callback-secret>" \
     -d '{}'
   ```
   You should get `2xx`. If you can't curl it, the server can't either.
4. **Use `https://webhook.site` as a temporary endpoint.** Register a
   Subscription with `endpoint` pointing at the webhook.site URL it
   gives you, trigger a change, watch the request appear in the
   webhook.site UI. If it fires there but not at your real endpoint,
   the problem is on your side (firewall, TLS, auth).

### "I got 401 trying to register the Subscription"

The token was rejected. Common causes:

- **Token expired.** Tokens are short-lived. Re-fetch.
- **Wrong scopes.** `system/Subscription.crus` is the minimum.
  Inspect your token's `scope` claim:
  ```bash
  echo "$TOKEN" | jq -R 'split(".")[1] | @base64d | fromjson | {scope, exp, iss, azp}'
  ```
- **Wrong issuer.** The server validates the JWT's `iss` claim against
  its configured issuer. If your token's `iss` doesn't match, ask
  your operator to confirm the issuer URL.
- **Token signed by the wrong key.** If you rotated your client secret
  but a cached token survives, you'll see this too. Re-fetch.

### "I got 422 when POSTing the Subscription"

The Subscription was syntactically OK but rejected by the channel
security policy. The `OperationOutcome` in the response body names the
specific issue. Common cases:

- `"endpoint must be https://"` — your `channel.endpoint` is HTTP.
  Switch to HTTPS.
- `"Authorization header required"` — your `channel.header` is empty
  or doesn't contain an `Authorization` line. Add one.
- `"channel.endpoint missing"` — channel `type` is `rest-hook` but no
  endpoint was set.

The full set of `422`s for channel security is documented in
[`architecture.md`](architecture.md) and the source for the
interceptor lives at
`hapi/auth/src/main/java/com/bzonfhir/subscriptionservice/channelsecurity/`.

### "How do I check delivery history?"

For now, this is limited:

- **`Subscription.status`** transitions to `error` (R4) when the
  channel is failing. Inspect the resource for the latest state.
- **`Subscription._history`** shows every server-side state change
  (status transitions). Useful for "when did this go bad."
  ```bash
  curl -fsS -H "Authorization: Bearer ${TOKEN}" \
    https://subscription-service.example.com/fhir/Subscription/${SUB_ID}/_history \
    | jq '.entry[].resource | {versionId: .meta.versionId, status, lastUpdated: .meta.lastUpdated}'
  ```
- A future ticket will add a `SubscriptionStatus`-based monitoring
  endpoint (Backport IG: `GET /fhir/Subscription/{id}/$status`) for
  inline delivery counters. Today that operation returns `not-supported`;
  fall back to the history endpoint above.

### "What's the retry behavior?"

The server retries failed deliveries with exponential backoff. Exact
parameters (max attempts, max total wait) are operator-configurable
and may be tuned by future hardening work; plan for "several attempts
spread across a few minutes" and design your endpoint accordingly.
After the retry budget is exhausted the Subscription's `status` moves
to `error`. To re-enable it, fix your endpoint and `PUT` the
Subscription back to `status: requested`.

## 9. Examples

Runnable examples are under [`examples/`](./examples/). Each script is
parameterized via environment variables so it works against any
deployment.

| Script | What it does |
|---|---|
| [`register-rest-hook-subscription.sh`](./examples/register-rest-hook-subscription.sh) | Register a `rest-hook` Subscription, watch for the response, print the assigned id. |
| [`register-websocket-subscription.sh`](./examples/register-websocket-subscription.sh) | Register a `websocket` Subscription, print connect instructions. |
| [`query-subscription-status.sh`](./examples/query-subscription-status.sh) | Read a Subscription's current state and full history. |

Each script reads its config from:

| Env var | Meaning | Default |
|---|---|---|
| `SUBSCRIPTION_SERVICE_URL` | FHIR base URL (no trailing slash) | `https://subscription-service.example.com/fhir` |
| `TOKEN` | OAuth2 bearer token | (empty — required unless auth is disabled) |
| `CALLBACK_URL` | Your webhook endpoint (for `register-rest-hook-subscription.sh`) | `https://webhook.site/<your-id>` |
| `CALLBACK_SECRET` | Bearer secret your callback will verify | a random value |

Minimal end-to-end smoke (using `webhook.site` as the callback):

```bash
# 1. Grab a fresh URL at https://webhook.site (it gives you a UUID).
export SUBSCRIPTION_SERVICE_URL=https://subscription-service.example.com/fhir
export TOKEN=$(curl -fsS -X POST \
  https://your-keycloak.example.com/realms/<realm>/protocol/openid-connect/token \
  -d "grant_type=client_credentials" \
  -d "client_id=<id>" \
  -d "client_secret=<secret>" \
  | jq -r .access_token)
export CALLBACK_URL=https://webhook.site/<your-uuid>
export CALLBACK_SECRET=$(openssl rand -hex 16)

./docs/examples/register-rest-hook-subscription.sh

# 2. Trigger a Patient change to fire the Subscription
curl -fsS -X POST \
  "${SUBSCRIPTION_SERVICE_URL}/Patient" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/fhir+json" \
  -d '{"resourceType":"Patient","name":[{"family":"Test"}]}'

# 3. Watch the request land in the webhook.site UI within a few seconds.
```
