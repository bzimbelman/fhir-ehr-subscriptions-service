# ADR 0005: Cancel-and-replace correlation lives in the adapter

**Status.** Accepted.

**Reader's prerequisites.** Read [../domains/ehr-adapter.md](../domains/ehr-adapter.md) (section "Cancel-and-replace as a first-class adapter concern") and `../../architecture.md` (section "Cancel-and-replace pairs" — canonical). Then [../domains/topic-matcher.md](../domains/topic-matcher.md) for what the matcher sees.

## Context

Some EHRs model an edit as a **cancellation of the prior resource plus creation of a new one with a fresh identifier**. Orders are the canonical case: an Epic order edit emits an HL7 cancel ORM (the original order, marked cancelled) followed by a new ORM with a fresh order number. The two messages are two halves of the same logical edit.

The wire shape carries no in-band link. The cancellation references the original order ID; the replacement carries a different order ID and there is no `replaces` reference (or, in the cases where there is, it is a vendor-specific field — Epic uses a placeholder order ID, Cerner uses a correlation ID in a vendor field, etc.).

The question is **where to correlate the pair**. Three options:

1. **Subscriber correlates.** The server emits two `resource_changes` rows (one delete, one create), the subscriber receives two notifications, and the subscriber figures out the link.
2. **Engine correlates.** The server emits two `resource_changes` rows, the topic matcher / engine recognizes the pair, and produces one notification.
3. **Adapter correlates.** The adapter recognizes the pair and emits one `resource_changes` row with `change_kind = update`, `previous_resource` = cancelled, `resource` = replacement.

Option 1 is the easiest implementation and the worst experience. **Subscribers cannot correlate the pair on their own** — the new resource has a different ID and there is no link in the spec wire shape. A subscriber receiving "delete order 12345" followed by "create order 67890" has no way to know order 67890 replaced order 12345. That linkage is vendor-specific knowledge — it is `ORC-2`/`ORC-3` placeholder/filler conventions, Epic's placeholder order ID, Cerner's correlation field. Pushing this onto every subscriber forces every subscriber to learn vendor-specific rules and re-implement what the adapter already knows. It also forces every subscriber to see "the order disappeared" as a transient state that may or may not be followed by a replacement; subscribers on `delete` topics fire for transient cancellations that are about to be superseded.

Option 2 is plausible but wrong. The engine and topic matcher are **vendor-neutral** by design. They do not know about Epic's placeholder order IDs or Cerner's correlation fields. Putting cancel-and-replace recognition in the engine would force the engine to grow a `match vendor` switch — exactly what the architecture's vendor isolation principle prevents. It would also make the engine's behavior depend on vendor-specific configuration, which is operator-confusing and test-expensive.

Option 3 — the adapter — is the only place with the vendor-specific knowledge to recognize the pair. The adapter already understands HL7 `ORC-2`/`ORC-3`, Epic's placeholder order ID, Cerner's correlation conventions. Doing the correlation there is a small addition to existing vendor-specific code; doing it elsewhere forces the vendor knowledge to leak into vendor-neutral components.

## Decision

**The adapter is responsible for collapsing cancel-and-replace pairs into a single `update` row.** The adapter writes one `resource_changes` row with `change_kind = update`, `previous_resource` set to the cancelled resource, and `resource` set to the replacement.

Mechanics (canonical in the architecture; HLD restates briefly):

- The vendor subclass's `classify(parsed)` returns the `vendor_correlation_key` that links the pair (HL7 `ORC-2`/`ORC-3`, Epic placeholder order ID, vendor-specific group ID).
- The base class's correlation-window state machine pairs the messages within `correlation_hold_window()` (default 30s, configurable per resource type).
- Pair recognized → one `resource_changes` row with both `previous_resource` and `resource` populated, plus a stable `correlation_id` bridging both source messages.
- Hold window expires with no partner → the configurable unpaired path: emit plain `delete` for an unpaired cancellation, plain `create` for an unpaired replacement.
- Held cancellations survive process restart: the source HL7 row in `hl7_message_queue` is **not** marked processed while the cancellation is held, and a small Postgres-backed pending table persists the in-flight pairing state. Restart replays the held cancellation and re-enters the correlation window.

The pairing logic itself is in the framework so vendor code only contributes the correlation key. The state machine, the hold window, the timeout, and the unpaired emission paths are inherited.

## Consequences

### Positive

- **Subscribers see ordinary `update` notifications.** The Bundle carries `previous_resource` and `resource`; the subscriber compares them directly. No vendor knowledge is required on the subscriber side.
- **Topic authors write ordinary transition criteria.** A topic written as "fires when `ServiceRequest` transitions from `active` to `revoked`" matches the merged update the same way it would match a true single-message update. The matcher does not have a special case.
- **Vendor knowledge stays in the adapter.** The engine, topic matcher, channel modules, and storage know nothing about cancel-and-replace conventions. Vendor isolation is preserved.
- **Cross-vendor consistency.** Every adapter that has cancel-and-replace semantics implements the correlation the same way (override `classify` to return the correlation key, inherit the framework's pairing). The behavior is the same on the subscriber side regardless of which EHR is the source.
- **Operators see one durable artifact.** A merged update is one row, with both states. Debugging is easier than chasing two related rows by metadata.

### Negative

- **Hold-window latency.** A cancel arriving without its replacement waits up to `correlation_hold_window` before emission. With the default 30s, an unpaired cancellation has a worst-case 30s delay before the subscriber sees the `delete`. We accept this — paired cancellations are the common case for the EHRs that have this pattern; the delay only affects the unpaired path.
- **Held state is durable but not free.** The pending table holds in-flight cancellations. If a vendor's interface is misbehaving and the EHR is sending cancellations without replacements en masse, the pending table grows. Operator metrics expose `fhir_subs_cancel_replace_pending` (see [../domains/observability.md](../domains/observability.md)) so this is visible and alertable.
- **Restart re-enters held state.** On restart, the framework re-loads the pending table and resumes the correlation window. This is correct behavior, but the framework has to be careful with timer accounting (a restart resets the 30s window — the held cancellation effectively gets fresh time). We accept this; the alternative (persisting a fine-grained timer state) is more complex than the bug it would prevent.

### Neutral

- **Bootstrap cases are handled.** A replacement that arrives without a preceding cancellation (because the cancellation was processed in a prior session, or the EHR doesn't always send the cancellation half) is emitted as a plain `create`. No vendor-specific config is required.
- **Configurability is per resource type.** An EHR that uses cancel-and-replace for orders but not for results can set `correlation_hold_window` per resource type. The framework supports this; the configuration domain owns the schema.
- **The decision is bounded to the adapter SPI.** Other components are unaware that cancel-and-replace exists. If a future spec or future vendor introduces a new edit pattern, the framework's pairing primitive can be reused for it; the engine does not need to change.
