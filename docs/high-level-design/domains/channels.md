# Channels

**Purpose.** The Channel domain. Each notification channel is a module that takes a built `subscription-notification` Bundle and delivers it via one protocol. Owns the per-protocol behavior (HTTPS POST, WSS frame, SMTP submit, FHIR messaging, custom) and reports a `DeliveryOutcome` back to the engine. Does not assemble Bundles, retry on its own, or own dead-letter policy.

**Reader's prerequisites.** Read [subscriptions-engine.md](subscriptions-engine.md) (the engine that hands envelopes to channels), [../contracts/channel-spi.md](../contracts/channel-spi.md) (the SPI signature), and `../../architecture.md` (sections "Channel SPI" and "Email Channel"). The R5 channel-type spec page is canonical: [`https://hl7.org/fhir/R5/codesystem-subscription-channel-type.html`](https://hl7.org/fhir/R5/codesystem-subscription-channel-type.html).

## What a channel is

A channel is a module that implements the [Channel SPI](../contracts/channel-spi.md). The SPI is a stable contract:

- `manifest()` — identity, supported payload types, supported content types, batching support, handshake support, configuration schema.
- `start(ctx)` / `shutdown()` — process-level lifecycle.
- `on_subscription_activated(sub)` / `on_subscription_deactivated(sub)` — per-subscription lifecycle, including the activation handshake.
- `deliver(envelope) -> DeliveryOutcome` — the only required call path from the engine.
- `send_heartbeat(sub)` — for channels that support heartbeats.

The core delivery scheduler hands a `NotificationEnvelope` (Bundle plus metadata) to the channel and trusts it to deliver. The channel returns one of three outcomes:

- `Delivered` — success.
- `TransientFailure { retry_after, reason }` — retry per the scheduler's policy.
- `PermanentFailure { reason }` — dead-letter.

The scheduler owns the retry curve, the backoff, and the dead-letter routing. Channels do not implement their own queues.

## The four spec-defined channel types

Per the R5 / R6 spec and the R4B Backport IG, four channel types are core, and additional types are permitted via the extensible binding on `Subscription.channelType`. Built-in modules ship for all four.

### `rest-hook`

HTTPS POST of the Bundle to the subscriber's `Subscription.endpoint` URL. The default and most widely deployed channel.

- **Endpoint shape.** `https://...` URL. Plain HTTP is rejected at subscription create.
- **Payload types.** `empty`, `id-only`, `full-resource` — all supported.
- **Content types.** `application/fhir+json` (default), `application/fhir+xml`.
- **Headers.** `Subscription.parameter` carries name-value pairs the subscriber wants attached to each POST (typically a bearer token they want echoed). Server-injected headers: `User-Agent`, `Content-Type`, the `correlation_id` for tracing.
- **Activation handshake.** Per spec: at activation, the server POSTs a handshake notification to the endpoint. Success (`2xx` response within the handshake timeout) transitions the subscription to `active`. Failure leaves it in `requested` with the error visible via `$status`.
- **Heartbeats.** Supported. A heartbeat is a Bundle containing only a `SubscriptionStatus` resource of `type = heartbeat`. Sent at `Subscription.heartbeatPeriod` cadence when no event has been delivered.
- **Failure mapping.** `2xx` → `Delivered`. `4xx` (except 408, 429) → `PermanentFailure`. `408`, `429`, `5xx`, connection errors, TLS errors → `TransientFailure`.

Configuration knobs (`channels.rest_hook` in the configuration domain): `request_timeout`, `max_retries` (advisory; the scheduler enforces), `backoff` curve, `user_agent`.

### `websocket`

Persistent WSS connections established by the subscriber. The server pushes notification frames to the connected client.

- **Endpoint shape.** The subscription does not carry a WebSocket URL the subscriber listens on; it carries a server-side WebSocket URL the subscriber connects to. The handshake is bootstrapped via the **`$get-ws-binding-token`** operation: the subscriber calls `POST /Subscription/{id}/$get-ws-binding-token` with their bearer token, the server returns a short-lived token, the subscriber upgrades a WSS connection to the server with that token, the server binds the connection to the subscription. See [`https://hl7.org/fhir/R5/operation-subscription-get-ws-binding-token.html`](https://hl7.org/fhir/R5/operation-subscription-get-ws-binding-token.html).
- **Payload types.** `empty`, `id-only`, `full-resource`.
- **Content types.** `application/fhir+json` (default), `application/fhir+xml`.
- **Activation handshake.** Per spec: when the subscriber's WSS connection binds to the subscription, the server emits a handshake notification frame on that connection. The subscription transitions to `active` once a connection is bound and the handshake has been emitted.
- **Heartbeats.** Supported. Plus a low-level WSS `ping`/`pong` for connection health (`channels.websocket.ping_interval`).
- **Disconnect handling.** A subscription whose WSS connection drops is not transitioned to `error` immediately — WebSocket disconnects are common. The subscriber is expected to reconnect via `$get-ws-binding-token` and rebind. Pending notifications are queued in `deliveries` while the connection is gone, up to a configured backlog cap; older events are recovered via `$events`.
- **Single-event-per-frame.** Each notification is one WSS frame. Batching (`maxCount > 1`) is supported because the spec allows multiple events per Bundle, and one Bundle still maps to one frame.

Configuration knobs (`channels.websocket`): `enabled`, `max_connections`, `ping_interval`, `idle_timeout`.

### `email`

SMTP submission of MIME-encoded notifications to a subscriber's mail address. The server is an SMTP **client**, not a server — there is no inbound SMTP listener and subscribers don't reply to notifications.

The full SMTP/S, S/MIME, and Direct stories live in `../../architecture.md` "Email Channel — How Mail Is Sent". Summary:

- **Endpoint shape.** `mailto:user@example.org` per `Subscription.endpoint`.
- **Three relay modes** (per-deployment, not per-subscription):
  1. **Plain SMTP / SMTPS** — STARTTLS or implicit TLS, SMTP AUTH.
  2. **S/MIME** — sign and optionally encrypt before submit.
  3. **Direct** — the healthcare-specific S/MIME profile with HISP-issued certificates and DNS/LDAP-based trust anchor discovery.
- **Payload types.** `empty`, `id-only`, `full-resource` — all supported. `full-resource` payloads are attached as `application/fhir+json` (or +xml) above a configurable inline-vs-attached threshold.
- **Activation handshake.** Email is not interactive; the spec's handshake here is a one-shot delivery and the server treats `Delivered` (relay accepted the message) as handshake success. There is no synchronous read-back of subscriber acknowledgement.
- **Heartbeats.** Supported but typically configured to a long period (e.g., daily) — high-frequency heartbeat email is operationally noisy.
- **Failure mapping.** SMTP 4xx → `TransientFailure`. SMTP 5xx → `PermanentFailure` for that recipient → dead-letter. Connection errors, TLS failures → `TransientFailure`. S/MIME signing/encryption failures fail the delivery before submit; the audit log records the failure.
- **Bounces.** Asynchronous DSN bounces are out of scope for v1. Operators monitor relay bounce reports separately.

Configuration knobs (`channels.email`): mode (`smtp` / `smime` / `direct`), `from`, `smtp.*`, `smime.*`, `direct.*`, `body.attachment_threshold_bytes`. See `../../architecture.md` configuration example.

### `message`

FHIR messaging channel: HTTPS POST of a FHIR `MessageHeader`-headed Bundle to a subscriber endpoint that speaks the [FHIR Messaging](https://hl7.org/fhir/R5/messaging.html) interaction.

- **Endpoint shape.** `https://...` URL accepting a FHIR `MessageHeader`-led Bundle.
- **Payload types.** `empty`, `id-only`, `full-resource`. The `MessageHeader` precedes the `subscription-notification` Bundle's standard entries.
- **Content types.** `application/fhir+json`, `application/fhir+xml`.
- **Activation handshake.** Per spec; same shape as rest-hook.
- **Heartbeats.** Supported.
- **Failure mapping.** Same as rest-hook.

The `message` channel is a thin variant of `rest-hook` whose body uses the FHIR messaging convention. Most subscribers use rest-hook; `message` exists for partners that already speak FHIR messaging.

## Custom channels

Per the R5 spec, `Subscription.channelType` has an extensible binding: "Additional channel types can be defined by external implementation guides." The Channel SPI gives custom channels the same status as built-in ones.

A custom channel implements the same SPI, registers a `Coding` with its own `system` + `code` (not `http://terminology.hl7.org/CodeSystem/subscription-channel-type`), and the `CapabilityStatement` advertises the new code. Subscribers create subscriptions with that `channelType` and the core delivery scheduler routes notifications to the custom channel.

Examples the architecture explicitly anticipates:

- **Kafka / NATS** — push to a topic for downstream pipelines.
- **MQTT** — IoT-style fanout.
- **gRPC** — low-latency partners.
- **SFTP drop** — batch consumers.
- **Vendor-proprietary push APIs** — partners that have their own ingest endpoint.

Custom channels are bundled into the container image at build time. Runtime plugin loading (WASM, dynamic libraries) is a stretch goal called out in the architecture's "Out of Scope".

## Per-subscription lifecycle

Each channel implements two per-subscription hooks:

- `on_subscription_activated(sub) -> HandshakeOutcome` — called by the core when a `Subscription` is created or when its channel-affecting fields are updated. The channel performs the activation handshake (POST a handshake Bundle, emit a WSS frame, send a handshake email) and returns success or failure. Success transitions the subscription to `active`; failure leaves it in `requested`.
- `on_subscription_deactivated(sub)` — called on `DELETE /Subscription/{id}` or on a server-side `off` transition. The channel cleans up any per-subscription state (closing WSS connections, removing scheduled retries).

For channels that have no per-subscription state (e.g., email — every delivery opens a fresh SMTP submit), these hooks may be no-ops.

## Heartbeats

Per the spec, `Subscription.heartbeatPeriod` is the cadence at which the server sends a `heartbeat` notification when no events have been delivered. The Channel SPI declares whether the channel supports heartbeats; the engine's delivery scheduler owns the timer.

- Heartbeat Bundles contain only a `SubscriptionStatus` of `type = heartbeat`. No `notificationEvent`. No payload.
- A heartbeat does NOT increment `eventsSinceSubscriptionStart`.
- A heartbeat that fails is treated like any other delivery failure: transient → retry, permanent → dead-letter and `error`/`off` transition.
- A subscription on a non-heartbeat channel that sets `heartbeatPeriod` is rejected at create time per the SPI's manifest declaration (HTTP 422).

## Content negotiation

The negotiated content type is per-subscription, set at create time via `Subscription.contentType` (or the `Accept` header if not explicit). The channel manifest declares supported content types; the API rejects subscriptions whose content type the channel does not support.

The Notification Builder serializes the Bundle in the negotiated content type and hands it on the envelope. Channels do not re-serialize.

## What this domain does NOT do

- **It does not build the Bundle.** That is the [Notification Builder](subscriptions-engine.md#stage-4--notification-builder) — Stage 4. Channels receive a complete envelope.
- **It does not own retry / backoff / dead-letter policy.** That is the delivery scheduler in the [Subscriptions Engine](subscriptions-engine.md#delivery-scheduler). Channels only report `DeliveryOutcome`.
- **It does not perform per-subscription filtering.** Filtering happens in Stage 3 before any envelope is built. By the time the channel sees a delivery, it is already destined for one specific subscriber.
- **It does not advance the event cursor.** The scheduler advances `eventsSinceSubscriptionStart` only on confirmed delivery.
- **It does not store delivery state.** Delivery rows live in `deliveries` and are owned by [storage](storage.md). Channels read what they need from the envelope.
- **It does not authenticate subscribers.** The [Subscriptions API](subscriptions-api.md) does that for management calls. The channel only authenticates *outbound* — its own credentials to the subscriber's endpoint or the SMTP relay.
