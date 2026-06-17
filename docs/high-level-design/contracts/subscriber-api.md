# Contract: Subscriber API

**Purpose.** The wire-level contract subscribers see. Resource shapes, operation signatures, version negotiation, error responses. The HLD-level reference for what the [Subscriptions API](../domains/subscriptions-api.md) exposes to the world.

**Reader's prerequisites.** Read [../domains/subscriptions-api.md](../domains/subscriptions-api.md) (the domain doc — what the API is responsible for and how it fits the rest of the system). Then this contract for the wire format.

The canonical specs are:

- FHIR R5 Subscription: [`https://hl7.org/fhir/R5/subscription.html`](https://hl7.org/fhir/R5/subscription.html).
- FHIR R5 SubscriptionTopic: [`https://hl7.org/fhir/R5/subscriptiontopic.html`](https://hl7.org/fhir/R5/subscriptiontopic.html).
- FHIR R5 SubscriptionStatus: [`https://hl7.org/fhir/R5/subscriptionstatus.html`](https://hl7.org/fhir/R5/subscriptionstatus.html).
- Subscriptions R4 Backport IG: [`https://hl7.org/fhir/uv/subscriptions-backport/`](https://hl7.org/fhir/uv/subscriptions-backport/).
- SMART on FHIR Backend Services: [`https://hl7.org/fhir/smart-app-launch/backend-services.html`](https://hl7.org/fhir/smart-app-launch/backend-services.html).

This contract document follows the spec; it does not extend it. If something below seems contradictory to the spec, the spec wins and this document is wrong.

## Endpoint surface

| Method | Path | What it does |
|---|---|---|
| `GET` | `/metadata` | Return the dynamic `CapabilityStatement`. |
| `POST` | `/Subscription` | Create a subscription. Triggers the activation handshake. |
| `GET` | `/Subscription` | Search subscriptions (per spec search parameters). |
| `GET` | `/Subscription/{id}` | Read one subscription. |
| `PUT` | `/Subscription/{id}` | Update a subscription. May trigger a re-handshake. |
| `DELETE` | `/Subscription/{id}` | Delete a subscription. Transitions to `off`. |
| `GET` | `/Subscription/{id}/$status` | Spec operation: current status. |
| `GET` | `/Subscription/$status?id=X&id=Y` | Spec operation: bulk status. |
| `GET` | `/Subscription/{id}/$events` | Spec operation: replay historical events. |
| `POST` | `/Subscription/{id}/$get-ws-binding-token` | Spec operation: WebSocket binding token. |
| `GET` | `/SubscriptionTopic` | Search topics. |
| `GET` | `/SubscriptionTopic/{id}` | Read one topic. |

`SubscriptionStatus` is never `POST`ed by subscribers. It is returned inside `subscription-notification` Bundles and as the response body of `$status`.

## Resource shapes

### `Subscription`

The R5 shape. The R4B Backport equivalent is a `Subscription` resource with the spec's profile extensions in `meta.profile` and the topic referenced via the backport's `topic` extension on R4 — the [version shim](../decisions/0004-fhir-version-strategy.md) translates between them.

Fields the server cares about (from [`https://hl7.org/fhir/R5/subscription.html`](https://hl7.org/fhir/R5/subscription.html)):

| Field | Required | Notes |
|---|---|---|
| `status` | yes | `requested` on create; transitions to `active` on handshake success. |
| `topic` | yes | Canonical URL of a `SubscriptionTopic` in the deployment's catalog. Must match a topic in `subscription_topics`. |
| `contact` | optional | Operator-visible contact for the subscriber. |
| `end` | optional | When the subscription should auto-disable. |
| `reason` | optional | Free-text. |
| `filterBy[]` | optional | Each entry: `{resourceType, filterParameter, comparator, modifier, value}`. Validated against the topic's `canFilterBy` at create time — a `filterParameter` not in `canFilterBy` is rejected with HTTP 422. |
| `channelType` | yes | A `Coding` from the `subscription-channel-type` extensible binding (or a custom-channel system). The server's `CapabilityStatement` enumerates supported codings. |
| `endpoint` | conditional | Required for channels that need an endpoint URL (rest-hook, message). Mailto for email. Absent for websocket (the binding is established via `$get-ws-binding-token`). |
| `parameter[]` | optional | Channel-specific parameters. For rest-hook: HTTP header name-value pairs. The Channel SPI manifest defines what each channel accepts. |
| `heartbeatPeriod` | optional | Integer seconds. Heartbeat cadence when no events are flowing. Bounded by `delivery.heartbeat.min_period` and `max_period`. |
| `timeout` | optional | Integer seconds. Maximum wait for a delivery attempt. Bounded by `channels.<type>.request_timeout`. |
| `maxCount` | optional | Maximum events per notification Bundle. Default 1 (no batching). Greater than 1 requires the channel to declare `supportsBatching = true`. |
| `content` | optional | One of `empty`, `id-only`, `full-resource`. Default `id-only`. |
| `contentType` | optional | One of `application/fhir+json` (default), `application/fhir+xml`. |

Fields the server returns but does not source from the subscriber:

| Field | Notes |
|---|---|
| `id` | Server-assigned. |
| `meta.versionId`, `meta.lastUpdated` | Standard FHIR meta. |
| Server-side cursor | Not on `Subscription`; exposed via `SubscriptionStatus.eventsSinceSubscriptionStart` (returned by `$status`). |

### `SubscriptionTopic`

Read-only to subscribers. The shape per [`https://hl7.org/fhir/R5/subscriptiontopic.html`](https://hl7.org/fhir/R5/subscriptiontopic.html). Subscriber-relevant fields:

| Field | Notes |
|---|---|
| `url` | Canonical URL. The value subscribers put in `Subscription.topic`. |
| `version` | Version of the canonical URL. Subscribers may pin (rare) or accept the current version. |
| `status` | `active` for topics subscribers may use. Retired topics are still returned for `$events` replay correctness. |
| `name`, `title`, `description` | Subscriber-facing documentation. |
| `resourceTrigger[]` | What the topic fires on. Read-only for subscribers — they do not write triggers. |
| `eventTrigger[]` | Vendor-event triggers (where adapter-contributed). |
| `notificationShape[]` | What `_include` / `_revinclude` the notification will carry. Read-only. |
| `canFilterBy[]` | Filter parameters subscribers may use in `Subscription.filterBy`. The whitelist. |
| `notificationShape[i].include` and `revInclude` | The reference data the topic will hydrate for `full-resource` payloads. |

### `SubscriptionStatus`

Returned inside notification Bundles and as the body of `$status`. Per [`https://hl7.org/fhir/R5/subscriptionstatus.html`](https://hl7.org/fhir/R5/subscriptionstatus.html). Fields the server populates:

| Field | Notes |
|---|---|
| `status` | Same value as the subscription's `status`. |
| `type` | One of `handshake`, `heartbeat`, `event-notification`, `query-status`, `query-event`. |
| `eventsSinceSubscriptionStart` | Server-assigned monotonic event counter for this subscription. |
| `notificationEvent[]` | One entry per included event. Each carries `eventNumber`, `timestamp`, `focus` (per payload type), and optionally `additionalContext`. |
| `subscription` | Reference to the source subscription. |
| `topic` | Canonical URL of the topic. |
| `error` | Populated when status is `error` — the most recent failure cause. |

The server emits all five spec-defined notification types — see [contracts/notification-bundle.md](notification-bundle.md) for the Bundle wire shape per type.

## Operations

### `$status`

[`https://hl7.org/fhir/R5/operation-subscription-status.html`](https://hl7.org/fhir/R5/operation-subscription-status.html).

Two forms:

- `GET /Subscription/{id}/$status` — status of one subscription.
- `GET /Subscription/$status?id=A&id=B&...` — bulk status.

Response: a Bundle of `searchset` type containing one `SubscriptionStatus` per requested subscription. Status type is `query-status`. Implemented as a read-only DB query against `subscriptions` and `deliveries`.

### `$events`

[`https://hl7.org/fhir/R5/operation-subscription-events.html`](https://hl7.org/fhir/R5/operation-subscription-events.html).

`GET /Subscription/{id}/$events?eventsSinceNumber=N&eventsUntilNumber=M&content=...`. Returns a `subscription-notification` Bundle of type `query-event` containing past `notificationEvent` entries within the configured retention window.

The replay reads `ehr_events` filtered by the subscription's topic, re-evaluates the subscription's `filterBy` against each (so a `filterBy` change between original delivery and replay is honored), re-checks authorization, and assembles the same way as live notifications. Payload type is the subscription's current `content` setting (or `?content=` override on the operation call, if the spec permits — the server honors the spec's behavior).

`$events` is the spec-blessed catch-up mechanism. Subscribers comparing their last-seen `eventsSinceSubscriptionStart` against the server's cursor (from `$status`) call `$events` to retrieve missed events.

### `$get-ws-binding-token`

[`https://hl7.org/fhir/R5/operation-subscription-get-ws-binding-token.html`](https://hl7.org/fhir/R5/operation-subscription-get-ws-binding-token.html).

`POST /Subscription/{id}/$get-ws-binding-token`. Returns a short-lived token plus the WebSocket URL the subscriber upgrades against. Subscriber connects to that URL, presents the token, the server binds the WSS connection to the subscription, and the channel emits the activation handshake frame.

The token is bound to the calling subscriber's `client_id` and to the subscription. It cannot be used by a different subscriber or for a different subscription.

## Authentication

**SMART on FHIR Backend Services only** ([`https://hl7.org/fhir/smart-app-launch/backend-services.html`](https://hl7.org/fhir/smart-app-launch/backend-services.html)). Asymmetric JWT.

Flow:

1. Subscriber's client mints a JWT, signs with its private key.
2. Subscriber posts to the configured token endpoint (the IdP's, not the server's — the server is a resource server, not an IdP) and receives an OAuth2 access token.
3. Subscriber presents the access token as `Authorization: Bearer <token>` on every API call.
4. The server validates the token against the configured `auth.trusted_issuers` (using each issuer's JWKS) and against the registered client (`auth.client_registry` from config). The token's `client_id` claim must match a registered client; the registered client's allowed scopes constrain what the token may authorize.

TLS is required on the API listener. Plain HTTP requests are rejected with `497`.

The architecture removed earlier mTLS-only and OAuth client-credentials alternatives during review; SMART Backend Services is the only supported scheme.

## Authorization (scopes)

Scopes follow the SMART pattern (`system/<Resource>.<crud>`). The server checks at two points (per spec):

- **At subscription create time.** The token's scopes must authorize the subscription's topic + filterBy + payload type. A token without `system/<TriggerResource>.r` cannot create an `id-only` or `full-resource` subscription that would surface bodies of that resource.
- **At delivery time.** Per spec, scopes are re-checked when each notification is being prepared. A subscriber whose access has been revoked stops receiving payloads. Failure transitions the subscription to `error` (or `off`, per policy).

The mapping from `<topic, filterBy, payload>` to required scopes is deployment policy. The server publishes its mapping in `CapabilityStatement.security` so subscribers can request the right scopes when minting tokens.

## CapabilityStatement contents

`GET /metadata` returns a dynamic `CapabilityStatement`. It enumerates:

- Supported FHIR versions.
- Supported `Subscription.channelType` codings (built-in plus loaded custom channels via the [Channel SPI](channel-spi.md) manifest).
- Supported payload types per channel.
- Supported `SubscriptionTopic` URLs (every `active` topic in the catalog).
- Supported operations: `$status`, `$events`, `$get-ws-binding-token`, plus standard CRUD.
- Auth scheme: `smart-on-fhir`.
- Spec-required search parameters on `Subscription` and `SubscriptionTopic` (see [../domains/subscriptions-api.md](../domains/subscriptions-api.md#search-parameters)).

The CapabilityStatement is built from loaded code and configuration at request time. It is not a static document.

## Version negotiation

Subscribers negotiate FHIR version via standard `Accept` and `Content-Type` headers. The server stores subscriptions in an R5-shaped internal model and translates at the wire — see [decisions/0004-fhir-version-strategy.md](../decisions/0004-fhir-version-strategy.md).

Rules:

- `Content-Type: application/fhir+json; fhirVersion=4.0` selects R4B Backport on input. Default for many subscribers today.
- `Content-Type: application/fhir+json; fhirVersion=5.0` selects R5 native.
- `Accept` controls the response version. If both R4B and R5 are accepted, the server prefers the version the subscription was originally created in.
- The negotiated version is recorded on the `subscriptions` row. Notifications for that subscription are emitted in that version regardless of what other subscribers receive.
- R6 is added to the shim once R6 publishes.

A subscriber that requests an unsupported FHIR version receives `406 Not Acceptable` with an `OperationOutcome`.

## Error model

Every error response is an `OperationOutcome` ([`https://hl7.org/fhir/R5/operationoutcome.html`](https://hl7.org/fhir/R5/operationoutcome.html)) with appropriate `severity` and `code`. The HTTP status code matches FHIR REST conventions.

| HTTP | When | OperationOutcome.issue.code |
|---|---|---|
| 400 | Malformed body, invalid resource per the spec. | `invalid` / `structure` |
| 401 | Missing or invalid bearer token. | `security` / `login` |
| 403 | Token valid but scopes do not authorize. | `forbidden` / `security` |
| 404 | Subscription or SubscriptionTopic not found. | `not-found` |
| 406 | FHIR version requested in `Accept` is not supported. | `not-supported` |
| 409 | Conflict on update (version mismatch). | `conflict` |
| 415 | Content-Type unsupported. | `not-supported` |
| 422 | Resource is structurally valid but semantically rejected — e.g., `maxCount > 1` on a non-batching channel, `filterBy` parameter not in the topic's `canFilterBy`. | `business-rule` / `processing` |
| 429 | Rate-limited. | `throttled` |
| 497 | Plain HTTP rejected. | `security` |
| 500 | Internal error. | `exception` |
| 503 | Server is not ready or is shutting down. | `transient` |

The body always includes a machine-readable `OperationOutcome.issue.code` plus a human-readable `OperationOutcome.issue.diagnostics`.

## Update semantics

`PUT /Subscription/{id}` is not a hot-swap. The server classifies the change and applies it in a way that keeps in-flight work consistent.

| Field changed | Server-side effect |
|---|---|
| `endpoint`, `header`, `channelType`, auth metadata | **Re-handshake.** Heartbeats and event-notifications pause until the new handshake succeeds. Existing pending deliveries on the old endpoint are dropped (and re-queued from `ehr_events` once the new endpoint is validated, where this is meaningful). |
| `filterBy`, `topic` | **Drain in-flight batch first.** New `ehr_events` rows stop being added to the subscription's pending batch. Any partially-built batch flushes under the *old* criteria, then the new criteria apply to subsequent events. The drain is bounded by `maxBatchWait`; if the engine cannot drain within ~60s, the in-flight batch is dropped and a metric is emitted. See [../domains/subscriptions-engine.md#update-semantics-drain-before-applying-changes](../domains/subscriptions-engine.md#update-semantics-drain-before-applying-changes). |
| `heartbeatPeriod`, `timeout`, `maxCount`, `content` | Take effect on the next notification scheduling round. No drain, no re-handshake. |
| `status` (subscriber-driven `off`) | Engine stops scheduling new deliveries; in-flight deliveries finish; the subscription transitions to `off`. |

Authorization is re-validated on every `PUT` regardless of which fields changed.

## Idempotency on subscription create

`POST /Subscription` is not strictly idempotent — repeated POSTs create distinct subscriptions. Subscribers wanting "create-or-update" semantics use the FHIR conditional create pattern: `POST /Subscription` with `If-None-Exist: criteria` where the criteria identify the subscriber's intended subscription (e.g., by `criteria` and `endpoint`).

`PUT /Subscription/{id}` is idempotent in the resource-state sense. Repeated PUTs with the same body produce the same `Subscription` state. The "drain on `filterBy`/`topic` change" rule above is a server-side mechanism, not a subscriber-visible state difference.

## Versioning policy for this contract

This contract is a thin wrapper around the FHIR Subscriptions spec; its versioning follows the spec's versioning, not a project-internal version.

- **Spec-aligned changes** — when HL7 publishes a new minor or major version of the spec, the server adds the new representation to the [version shim](../decisions/0004-fhir-version-strategy.md) and updates the `CapabilityStatement` to advertise it. Subscribers can opt in via `Accept`. Old subscribers continue to work in their negotiated version.
- **Server-policy changes** — the deployment's policy (which scopes are required for which payload type, which topics are loaded, which channels are enabled) is published in the live `CapabilityStatement`. Subscribers query `/metadata` to discover changes; the server does not version-pin its policy.
- **Breaking changes to the server's own surface** — for example, changing the spec-required search parameter set the server indexes — would be a major release and would be announced in the project's release notes. We do not anticipate such changes during the spec's current published versions; they would only happen if a future spec version mandated them.
- **Custom channels and adapter-contributed topics** are added and removed without a server version bump. Subscribers see them in `CapabilityStatement` and behave accordingly.

The contract makes no promises beyond the FHIR Subscriptions spec. If a subscriber wants behavior the spec does not require, they must propose it for a future spec version (or a spec-blessed extension); the server will not add it as a project-private extension.
