# ADR 0003: MLLP listener is host-provided and vendor-neutral

**Status.** Accepted.

**Reader's prerequisites.** Read [../domains/mllp-listener.md](../domains/mllp-listener.md) and `../../architecture.md` (sections "EHR side" and "MLLP Listener — host-provided MLLP queue").

## Context

EHRs that emit HL7 v2 messages do so over MLLP/TCP (Minimal Lower Layer Protocol). The receiver's job has two layers:

1. **MLLP framing.** Read the start byte (`<VT>` = `0x0B`), read until the end bytes (`<FS><CR>` = `0x1C 0x0D`), persist the body, ACK or NACK back to the sender. The HL7 v2 protocol on the EHR side guarantees that an un-ACKed message is held and re-sent.
2. **HL7 message parsing and translation.** Lex segments and fields, handle vendor-specific Z-segments, derive change kind from `MSH-9` / `ORC-1`, map to FHIR.

Layer 1 is **the same code regardless of which EHR is sending**. The MLLP framing is a vendor-independent standard. Layer 2 is irreducibly vendor-specific — Z-segments, vendor coding systems, vendor-specific cancel-and-replace conventions, and per-vendor field-mapping quirks.

The natural design question: should layer 1 live in the host (vendor-neutral, written once) or in each adapter (per-vendor, written N times)?

Putting layer 1 in the adapter is plausible at first glance because "the adapter knows about HL7." But every adapter would have to re-implement:

- the listener loop and TCP socket handling;
- MLLP framing (every adapter would re-implement the same byte-level parser);
- the persistence-then-ACK durability invariant (re-implementing this wrong loses messages or duplicates them);
- the multi-endpoint configuration (most facility interface engines open separate connections per message type);
- the backpressure / NACK / drop-connection model;
- the per-message metadata capture (`received_at`, `listener_endpoint`, `peer_addr`, `mllp_message_id`, `correlation_id`);
- TLS for MLLP-over-TLS deployments.

Bugs in any of these would be per-adapter. A subtle MLLP framing bug in one adapter would not be caught by tests in another adapter. The durability invariant — receive, persist, ACK, in that order — would have to be tested per-adapter.

The trade-off is asymmetric. Layer 1 in the host costs: one well-tested implementation. Layer 1 per-adapter costs: N implementations, N maintenance burdens, N possible places to lose messages.

The architecture commits to vendor-neutral by design. The HLD records that decision so the trade-off is visible.

## Decision

The **MLLP listener is host-provided and vendor-neutral**. It accepts MLLP/TCP connections, frames messages, persists raw bytes plus metadata to `hl7_message_queue`, and ACKs the EHR. It does no parsing beyond reading `MSH-9` (for the optional `allowed_message_types` filter) and `MSH-10` (for the `mllp_message_id` field).

All HL7 message parsing and translation lives in the adapter's `Hl7MessageProcessor`, which reads from `hl7_message_queue` and writes to `resource_changes`. Vendor knowledge — Z-segments, change-kind derivation, mapping, validation — is exclusively in the adapter.

The listener supports multiple configured endpoints per deployment (one bind+port per HL7 message type, matching how facility interface engines actually deploy) but writes them all to the same queue table with `listener_endpoint` metadata.

## Consequences

### Positive

- **One well-tested MLLP implementation.** Bugs in framing, persistence-then-ACK, NACK semantics, or backpressure are fixed once. Every adapter benefits.
- **Adapters are smaller.** A new vendor adapter does not have to ship a TCP server. It implements `Hl7MessageProcessor.lex()`, `classify()`, `map_to_fhir()`, and inherits everything else.
- **Durability invariant is enforced uniformly.** Receive, persist, ACK — in that order — is the listener's contract and is the same for every adapter.
- **Multi-endpoint deployments work the same way for every adapter.** A facility with 5 separate MLLP feeds (ADT, ORM, ORU, SIU, MDM) configures 5 listener endpoints; the active adapter sees them as one queue with `listener_endpoint` metadata.
- **Operators run one MLLP listener configuration.** Switching adapters does not change the MLLP-side wiring. A facility that swaps `epic` for `meditech-expanse` changes one config field; the MLLP listener configuration stays the same.

### Negative

- **The listener does not do any HL7 sanity-checking.** A malformed message that frames cleanly is accepted, persisted, and ACKed. The adapter catches the malformation later, dead-letters the message, and the framework moves on. This is the correct behavior — refusing to ACK because of a content problem would force the EHR to retry indefinitely — but operators sometimes expect "the receiver caught the bad message." The right answer is "the dead-letter table caught the bad message."
- **The metadata schema is one-size-fits-all.** Some vendors might want to capture additional per-message metadata at receive time. The architecture's metadata fields are sufficient for current adapters; if a future adapter genuinely needs more, the listener's metadata schema is extended, not adapter-replaced.
- **The listener cannot apply vendor-specific filtering at the wire.** A vendor that wants to drop certain `MSH-3`-tagged feeds cannot do so in the listener; it must do so in the adapter (which then dead-letters the dropped messages). In practice this is fine — the filter `allowed_message_types` covers the common case (drop unwanted message types per endpoint) and the adapter handles fine-grained filtering downstream.

### Neutral

- **MLLP-over-TLS** is an opt-in per endpoint. The listener handles both plain MLLP and TLS-MLLP; the adapter does not.
- **The listener is the durability boundary.** Once it ACKs, the message is the server's responsibility. The architecture's words: "Once ACKed, the message will not be lost."
- **This decision applies to MLLP only, not to FHIR REST or vendor APIs.** Those are owned by the adapter (FHIR Scan Runner, Vendor API Client) because every EHR's FHIR profile, search-parameter behavior, paging quirks, and auth flow differ. Putting FHIR REST in the host would be the inverse mistake — vendor-specific behavior in vendor-neutral code.
