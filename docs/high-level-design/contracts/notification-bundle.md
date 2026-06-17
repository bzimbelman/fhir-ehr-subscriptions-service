# Contract: Notification Bundle

**Purpose.** The wire shape of every notification the server sends — handshakes, heartbeats, event-notifications, query-status, query-event. All five share the same `Bundle` of type `subscription-notification`, with structural variations driven by `(notification type, payload type, topic shape, batching state)`.

**Reader's prerequisites.** Read [../domains/subscriptions-engine.md](../domains/subscriptions-engine.md) (the producer) and `../../architecture.md` (section "Notification Construction"). The canonical spec page is [`https://hl7.org/fhir/R5/notifications.html`](https://hl7.org/fhir/R5/notifications.html).

This contract is what every channel module ([Channel SPI](channel-spi.md)) carries on the wire. The server produces it; the channel delivers the bytes.

## Bundle skeleton

Every notification is a FHIR `Bundle` of type `subscription-notification` ([`https://hl7.org/fhir/R5/bundle-definitions.html#Bundle.type`](https://hl7.org/fhir/R5/bundle-definitions.html#Bundle.type)). Skeleton:

```
Bundle {
  resourceType: "Bundle"
  type: "subscription-notification"
  timestamp: <ISO 8601 server-side time>
  entry: [
    { resource: SubscriptionStatus { ... } },     // ALWAYS first; ALWAYS exactly one
    { resource: <focus or included resource> },   // zero or more, depending on payload type
    ...
  ]
}
```

Invariants:

- The first entry is **always** a `SubscriptionStatus`. The spec is explicit; the architecture restates it.
- There is **exactly one** `SubscriptionStatus` per Bundle (regardless of how many `notificationEvent` entries it contains).
- Subsequent entries are zero or more focus / included resources, depending on payload type.
- `Bundle.timestamp` is the server-side time at which the Bundle was assembled, not the time the underlying EHR change occurred (`occurred_at` is on the `notificationEvent`).

## SubscriptionStatus

The first Bundle entry. Per [`https://hl7.org/fhir/R5/subscriptionstatus.html`](https://hl7.org/fhir/R5/subscriptionstatus.html). Fields the server populates:

| Field | Notes |
|---|---|
| `status` | The subscription's current status (`active`, `error`, etc.). |
| `type` | One of `handshake`, `heartbeat`, `event-notification`, `query-status`, `query-event`. Selects the Bundle variant. |
| `eventsSinceSubscriptionStart` | Server-assigned monotonic event counter. Reflects the highest event number conveyed by this Bundle (or the current cursor for non-event Bundle types). |
| `notificationEvent[]` | Zero or more entries, one per included event. |
| `notificationEvent[i].eventNumber` | Per-subscription monotonic event number. |
| `notificationEvent[i].timestamp` | When the event occurred (the `occurred_at` from `ehr_events`). |
| `notificationEvent[i].focus` | Reference to the resource that triggered the event. Absent for `empty` payload type. |
| `notificationEvent[i].additionalContext` | References to included resources (per the topic's `notificationShape`). Absent for `empty` and `id-only`. |
| `subscription` | Reference to the subscription. |
| `topic` | Canonical URL of the topic. |
| `error` | Populated only when `status = error` — the most recent failure cause. |

## Five notification types

| Type | When | Increments `eventsSinceSubscriptionStart`? | Carries event payload? | Bundle entries beyond `SubscriptionStatus` |
|---|---|---|---|---|
| `handshake` | At subscription activation. | No | No | None. The Bundle is `SubscriptionStatus`-only. |
| `heartbeat` | At `Subscription.heartbeatPeriod` cadence when no events have been delivered in the period. | No | No | None. |
| `event-notification` | When matching events have occurred. The primary path. | **Yes** | Yes (per `Subscription.content`) | Per payload type — see below. |
| `query-status` | Server response to `GET /Subscription/{id}/$status`. | No | No | None. |
| `query-event` | Server response to `GET /Subscription/{id}/$events?...`. | No | Yes | Per payload type, replayed from `ehr_events`. |

Heartbeat and handshake share the same minimal Bundle shape: just the `SubscriptionStatus` with `type` set accordingly and no `notificationEvent` entries. Query-status returns one or more `SubscriptionStatus` resources for the requested subscriptions (potentially in a `searchset`-shaped Bundle if multiple ids are queried; see `../domains/subscriptions-api.md`). Query-event has the same structure as event-notification; the difference is `type` and the source (durable replay vs. live).

## Payload-type variations

`Subscription.content` (one of `empty`, `id-only`, `full-resource`) controls what goes into the Bundle alongside `SubscriptionStatus`.

### `empty`

```
Bundle {
  type: "subscription-notification"
  entry: [
    SubscriptionStatus {
      type: "event-notification"
      notificationEvent: [{ eventNumber, timestamp /*, no focus */ }]
    }
  ]
}
```

`notificationEvent[i].focus` is **absent**. No resource entries beyond the `SubscriptionStatus`. Subscribers learn that an event happened and use `$events` (or pull from the EHR with their own credentials) for details.

### `id-only`

```
Bundle {
  type: "subscription-notification"
  entry: [
    SubscriptionStatus {
      type: "event-notification"
      notificationEvent: [{ eventNumber, timestamp, focus: Reference("ServiceRequest/abc-123") }]
    }
  ]
}
```

`notificationEvent[i].focus` references the matching resource. No resource bodies. Subscriber follows the reference to retrieve the body.

### `full-resource`

```
Bundle {
  type: "subscription-notification"
  entry: [
    SubscriptionStatus {
      type: "event-notification"
      notificationEvent: [{
        eventNumber, timestamp,
        focus: Reference("ServiceRequest/abc-123"),
        additionalContext: [Reference("Patient/123")]
      }]
    },
    { resource: ServiceRequest { id: "abc-123", ... } },
    { resource: Patient        { id: "123",     ... } }
  ]
}
```

`SubscriptionStatus.notificationEvent[i].focus` references the primary resource; that resource appears as a Bundle entry; resources required by the topic's `notificationShape` (`_include` / `_revinclude`) also appear as entries and are referenced from `additionalContext`.

The Notification Builder calls the [Hydration Service](../domains/ehr-adapter.md#hydration-service) for each referenced resource the topic shape requires. The hydration service caches across subscriptions matching the same event so the EHR is fetched once per reference.

## Ordering invariants

Hard rules the engine enforces:

- `SubscriptionStatus` is **always entry index 0**. Channels MUST NOT reorder entries.
- For Bundles with multiple `notificationEvent` entries (batching), the entries are ordered by `eventNumber` ascending.
- For `full-resource` Bundles, the focus resource(s) appear before included resources (no spec requirement, but a deterministic order helps subscribers).
- `SubscriptionStatus.eventsSinceSubscriptionStart` reflects the highest `eventNumber` in the batch.

## Batching — `Subscription.maxCount`

A single Bundle can carry multiple events for the same subscription when `Subscription.maxCount > 1`. The spec is explicit that `maxCount` caps **events**, not Bundle entries — included Patient and Practitioner resources do not count toward the cap.

A batched Bundle:

- Contains one `SubscriptionStatus` (always).
- Contains one `notificationEvent[i]` per event in the batch.
- Contains one focus resource entry per event for `id-only` / `full-resource` payloads (deduplicated if the same resource appears in multiple events — e.g., two updates to the same `ServiceRequest`).
- Contains one included resource entry per `notificationShape` reference, deduplicated across the batch (so a batch of three orders for the same Patient hydrates Patient once).

The batching state machine and the `maxBatchWait` cap are owned by the [delivery scheduler](../domains/subscriptions-engine.md#delivery-scheduler). The wire shape of a batched Bundle is the same as a single-event Bundle, just with more entries.

## `query-event` notifications

`$events` returns a Bundle of type `subscription-notification` with `SubscriptionStatus.type = query-event`. The Bundle structure is identical to live event-notification Bundles for the same `(payload type, topic shape)`; the differences are:

- Sourced from durable replay of `ehr_events` (within retention) rather than from a live `deliveries` row.
- Filter is re-evaluated against the current subscription state (so a `filterBy` change between original delivery and replay is honored).
- Authorization is re-checked.
- `eventsSinceSubscriptionStart` does NOT advance — replay does not consume events.

A subscriber that catches up via `$events` and then receives live notifications sees a continuous `eventNumber` sequence: replay returns events the subscriber already knows the numbers for; live continues from the cursor.

## Content type

The Bundle is serialized in the negotiated content type: `application/fhir+json` (default) or `application/fhir+xml`. The Notification Builder produces the bytes; channels do not re-serialize.

The negotiated content type is recorded on the `subscriptions` row at create time and is constant for the life of the subscription. A `PUT /Subscription/{id}` that changes `contentType` triggers a re-handshake.

## Versioning policy for this contract

The Bundle wire shape is defined by the FHIR Subscriptions spec. Versioning rules:

- **Spec versions.** R4B Backport, R5, and (when published) R6 each define their own representation of `Bundle.type = subscription-notification` and `SubscriptionStatus`. The [version shim](../decisions/0004-fhir-version-strategy.md) emits the negotiated version per subscription. Subscribers do not see version drift within a subscription's lifetime; they see the version they negotiated.
- **Spec field additions.** When the spec adds a field (e.g., new `SubscriptionStatus.notificationEvent` sub-elements), the server adds support and emits the new field for subscribers on the new version. Subscribers on older versions continue to receive the older shape.
- **Server-controlled fields.** Fields the server fills (`Bundle.timestamp`, `SubscriptionStatus.eventsSinceSubscriptionStart`, etc.) follow the spec's semantics. The server does not add private extensions; if behavior beyond the spec is needed, it must be proposed through the spec process.
- **Breaking spec changes.** A future spec version that changes the Bundle invariants (different ordering, different first entry) would break subscribers; the version shim isolates older subscribers and the new shape is offered only to subscribers that negotiate the new version.
- **Custom channels / custom topics** do not change the Bundle wire shape. The Bundle is identical regardless of which channel delivers it or which topic produced it.

The Bundle contract is deliberately spec-bounded. See [decisions/0007-spec-bounded-scope.md](../decisions/0007-spec-bounded-scope.md): we do not add features the FHIR Subscriptions spec does not require.
