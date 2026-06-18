# 0011 — Message-channel Bundle bytes are deterministic via typed-struct serialization

**Status:** Accepted

## Context

The message channel (`internal/channel/message/message.go`) is the only built-in channel that re-serializes the notification Bundle on the way out — the FHIR messaging interaction requires wrapping the inner subscription-notification Bundle in a `Bundle.type=message` envelope with a synthesized `MessageHeader` at `entry[0]` (LLD §4.4).

The original implementation built the outer Bundle out of `map[string]interface{}` values and stamped the `Bundle.timestamp` from `time.Now()`. Two problems followed:

1. **`time.Now()` and `uuid.NewString()` are non-deterministic for the same input.** Wrapping the same envelope twice produced different bytes because the timestamp moved and (when the inner Bundle had no `id`) a fresh UUID was minted on each call.
2. **`map[string]any` is fragile.** Go's `encoding/json` does sort map keys, so simple structures were stable in practice — but the contract is implicit. Any reader of the code has to verify, every time, that no helper, override, or mutation could leak iteration order onto the wire. A typed struct makes the contract explicit and tooling-checkable.

The audit doc (`docs/production-readiness-audit.md` N-1.9) called this out as a sink-side de-dup hazard: subscribers comparing `SHA-256` of the body see false negatives across re-deliveries, and the planned audit-chain `chain_input` over message-channel deliveries would be unstable for the same input.

The mirror channel — `internal/engine/builder/builder.go` (B-28) — had already adopted typed-struct serialization with `json.RawMessage` for inner content. The builder's pattern, already covered by `internal/engine/builder/determinism_test.go`, served as the template.

## Decision

The message channel emits **byte-deterministic outer Bundle bytes for the same input** by combining three changes, none of which is a full RFC 8785 (JCS) canonicalizer:

1. **Typed structs for the synthesized envelope and MessageHeader.** `outerBundle`, `outerBundleEntry`, `messageHeader`, `messageHeaderCoding`, `messageHeaderEndpoint`, and `messageHeaderFocus` replace the previous `map[string]interface{}` literals. Field order on the wire is the canonical FHIR Bundle order (`resourceType`, `type`, `timestamp`, `entry`).
2. **Inner-entry passthrough via `json.RawMessage`.** The inner Bundle's entries are unmarshaled into `[]json.RawMessage`, then each entry's `resource` field is extracted as `json.RawMessage` and re-emitted byte-for-byte. The channel never re-marshals adapter-produced resource bodies, so canonicalization stays upstream of the channel — same posture as `builder.go` per its docstring at line 229.
3. **Injectable Clock and NewID.** `Options.Clock` (defaults to `time.Now`) sources `Bundle.timestamp`. `Options.NewID` (defaults to `uuid.NewString`) mints the inner Bundle's UUID when the inner Bundle lacks an `id`. Tests and the audit-chain replay path can inject deterministic implementations; production code paths see no behavior change.

Together these three changes pin the determinism contract: with a fixed Clock and NewID, wrapping the same envelope yields byte-identical wire bytes. The new tests in `internal/channel/message/determinism_test.go` and `e2e/orchestrator/channels_message_hardening_test.go` lock this in across 100 wraps.

## Why not full RFC 8785 (JCS)?

ADR 0010 §3 commits the project to RFC 8785 wherever JSON is **hashed or signed**. The hash / signature surfaces are: FHIR Scan Runner `content_hash`, audit-log `prev_hash`, and read-mostly `ETag`s. These are off-wire computations whose stable input is the entire JSON document.

The message channel produces an on-wire body. Its determinism need is narrower:

- **No floating-point fields.** The outer Bundle has no numeric resources. RFC 8785's IEEE-754 number canonicalization is inert here.
- **String encoding is already canonical.** Go's `encoding/json` emits `\uXXXX`-escaped strings with the same shape RFC 8785 prescribes for the cases we hit (no surrogate pairs, no decimal-form whitespace).
- **The inner-entry resources pass through verbatim.** If a hashing surface needs canonical form for those resources, that hashing surface — not the message channel — applies JCS upstream. The channel must not re-shape adapter-produced bytes (LLD §4.4 explicitly forbids it).

Adding a full JCS pass at the channel seam would re-marshal the inner-entry resources. That is the wrong place: it forces every adapter to participate in the channel's canonicalization rules, breaks the "channel preserves bytes" contract from `builder.go`, and ships duplicate canonicalization code already present in the audit / hash paths.

The narrower contract — typed structs for the synthesized wrap, raw-message passthrough for inner content, injectable non-determinism sources — gives the same byte-stability guarantee where it matters (within a single deployment, for a single envelope) without paying the JCS tax at the wrong layer.

## Consequences

**Positive.**

- A subscriber-side de-dup hash over the wire body is meaningful for a re-delivered envelope.
- The planned audit-chain `chain_input` over message-channel deliveries can compute against `wrapInMessageBundle` output without re-running JCS.
- Tests can pin byte-stability with a fixed Clock + NewID, mirroring the engine/builder pattern.

**Negative.**

- The channel grows two new `Options` fields (`Clock`, `NewID`). Defaults preserve the existing behavior; existing callers do not need to change.
- Two canonicalization regimes coexist in the codebase: typed-struct + raw-message at the channel seam, JCS at the hash / signature seam. The boundary is documented here and at the relevant code sites; future readers should not conflate them.

**Out of scope.**

- The XML serializer cut from v1 (rejected at the channel seam) is unaffected.
- The audit-chain canonicalization (ADR 0010 §3) remains JCS. Any future cross-deployment de-dup that needs JCS-level stability must invoke `internal/infra/observability/audit` rather than re-implementing it in the channel.

## Implementation

- `internal/channel/message/message.go` — typed structs (`outerBundle`, `messageHeader` family), `Options.Clock`, `Options.NewID`, `wrapInMessageBundle` rewritten to use them.
- `internal/channel/message/determinism_test.go` — three RED-then-GREEN tests: no inner id (NewID exercised), inner id present (NewID not exercised), map-heavy focus resource.
- `e2e/orchestrator/channels_message_hardening_test.go` — `TestE2E_Message_BundleBytesDeterministic` exercises the same contract through the orchestrator harness build tag.

## Story

This decision lands story #59 (audit doc N-1.9 "deterministic message-channel Bundle bytes"). The audit's "JCS-canonicalizer or sorted-keys writer" wording suggested a heavier intervention than is needed; this ADR records the lighter choice and the reasoning.
