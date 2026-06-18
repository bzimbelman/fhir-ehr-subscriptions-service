# Subscription Sidecar Demo

**Goal:** A live, two-terminal demo where:

1. A **subscriber** registers a FHIR Subscription with the bridge.
2. A **publisher / EHR simulator** sends HL7 v2 messages to the bridge over MLLP.
3. The subscriber receives notification Bundles for the messages that match its subscription's topic + filter.
4. The subscriber does **not** receive notifications for messages that should be filtered out.
5. The split is visible in real time across two terminal windows.

This document covers what we'd need to assemble that demo from what's already on `main` plus a small amount of new glue. It calls out the gaps explicitly so a contributor can scope the work.

---

## What's already on main that the demo needs

**Server side (the bridge itself):**
- `cmd/fhir-subs` — the binary entry point
- MLLP listener (`internal/mllp/`) — accepts HL7 v2 messages
- HL7 message processor (`internal/hl7processor/`) — translates HL7 to `resource_changes`
- Topic matcher (`internal/matcher/`) — matches against the topic catalog
- Subscriptions engine (`internal/engine/{submatcher,builder,scheduler}/`)
- REST-hook channel (`internal/channel/resthook/`) — what the demo subscriber will use
- Subscriptions API (`internal/api/`) — `POST /Subscription`, `$status`, `$events`
- Storage layer with all 13 tables and migrations
- Default reference adapter (`adapters/default/`) — passthrough lexer
- Topics catalog (`internal/topics/catalog/`) — embedded JSON Schema validation

**Test/harness side (reusable demo bits):**
- `e2e/mockehr/` — HL7 v2 builders + MLLP framing client (we'll wrap this for the publisher terminal)
- `e2e/mocksub/` — REST-hook receiver (we'll wrap this for the subscriber terminal)
- `e2e/harness/` — pipeline runner with all stages wired together (this is the testbench template)

---

## What's missing — gaps to close before the demo runs

### Gap 1 — Production binary doesn't serve the FHIR API — RESOLVED

**Source:** Production-readiness audit B-4

**Status:** RESOLVED via B-4 full production wiring (merged in `e615c31`, branch `fix/b4-full-production-wiring`). `cmd/fhir-subs/wiring.go::buildProductionRuntime` now constructs the DB pool, AES-GCM codec, SMART Backend Services verifier, and chi router with `handlers.RegisterRoutes` mounted; `RegisterPublicRoutes` exposes `/metadata` outside auth. The production binary serves the full Subscription API end-to-end and the demo can rely on it.

### Gap 2 — Production binary doesn't start the pipeline workers — RESOLVED

**Related to Gap 1:** RESOLVED via the same B-4 full production wiring (`e615c31`). `cmd/fhir-subs/wiring.go` (around lines 311-315) launches all four pipeline workers (`hl7processor.Worker`, `matcher.Worker`, `submatcher.Worker`, `scheduler.Worker`) plus the MLLP listener, gates `/readyz` on the lifecycle Probes, and lifecycle phases drive a clean shutdown. B-1 (readyz hardcoded 503) and B-3 (healthz flips OK too early) were closed earlier in `8096936`.

### Gap 3 — No CLI publisher tool

**Source:** none — would be net new for the demo

**What the demo needs:** a small `cmd/demo-publisher/` binary that:
- Connects to the MLLP listener on a configured host:port
- Reads HL7 v2 messages from a YAML/JSON catalog (or stdin)
- Sends them with timing the operator can see (one per second, one per keypress, or scripted)
- Logs each send + the ACK with a colored prefix so it's obvious in the terminal

**Why we need it:** the e2e harness has `e2e/mockehr/` which can do this in code, but it's not invokable from a shell. A demo needs `./demo-publisher --catalog scenarios/labs.yaml`.

**Estimated work:** small — wraps existing `e2e/mockehr/` packages.

**Suggested message catalog for the demo:**

```yaml
# scenarios/labs.yaml — used by the publisher
messages:
  - description: "Lab result for patient ABC123 (matches subscription)"
    delay: 1s
    template: oru-r01
    fields:
      patient_id: "ABC123"
      observation_code: "718-7"  # Hemoglobin
      value: "13.5"
      unit: "g/dL"

  - description: "Lab result for patient XYZ999 (filtered OUT — wrong patient)"
    delay: 2s
    template: oru-r01
    fields:
      patient_id: "XYZ999"
      observation_code: "718-7"
      value: "12.0"

  - description: "Encounter admit for ABC123 (filtered OUT — wrong topic)"
    delay: 3s
    template: adt-a01
    fields:
      patient_id: "ABC123"
      facility: "Demo Hospital"

  - description: "Lab result for patient ABC123 (matches subscription)"
    delay: 4s
    template: oru-r01
    fields:
      patient_id: "ABC123"
      observation_code: "789-8"  # RBC count
      value: "4.5"
      unit: "10^6/uL"
```

### Gap 4 — No CLI subscriber tool

**Source:** none — would be net new for the demo

**What the demo needs:** a small `cmd/demo-subscriber/` binary that:
- Mints a SMART Backend Services JWT against the bridge's `/token` endpoint (or accepts a static token via flag for the demo)
- POSTs a Subscription with rest-hook channel pointing at its own listener
- Hosts a local HTTP server that accepts the bridge's POSTs
- Pretty-prints each received Bundle to stdout with patient/topic/event-number, color-coded by topic

**Why we need it:** the e2e harness has `e2e/mocksub/resthook.go` (a real rest-hook receiver) but it's not invokable from a shell. A demo needs `./demo-subscriber --topic http://demo.org/topics/lab-results --filter patient=ABC123`.

**Estimated work:** small — wraps existing `e2e/mocksub/resthook.go` + adds a `POST /Subscription` client.

### Gap 5 — Docker-compose for one-command spin-up — RESOLVED

**Source:** none — net new for the demo (OpenProject #82)

**Status:** RESOLVED. `demo/docker-compose.yml` brings up `postgres:16-alpine`
plus a `bridge` service built from the repo's existing `Dockerfile`. The
bridge container mounts `demo/config.yaml` (insecure HTTP, MLLP listener
on `:2575`, pinned demo AES-GCM codec key, no audience -> no-op auth
middleware) and `demo/topics/` (the SubscriptionTopic JSON seeded by
gap-6, OpenProject #83) read-only at the paths the production wiring
expects. `cd demo && docker compose up -d` brings the bridge to
`/readyz` 200 within ~60s. See `demo/README-compose.md` for a focused
reference and `demo/README.md` (gap-10) for the operator walkthrough.

The compose stack is multi-arch (postgres:16-alpine + a
source-built bridge image), so it works on Apple Silicon and amd64
Linux/Mac without a `platform:` override.

### Gap 6 — Demo topic catalog

**Source:** none — would be net new for the demo

**What the demo needs:** a topics catalog file under `demo/topics/` with at least:

```yaml
# demo/topics/lab-results.yaml
url: "http://demo.org/topics/lab-results"
version: "1.0"
status: active
title: "Lab results"
resourceTrigger:
  - resource: Observation
    supportedInteraction: [create, update]
    queryCriteria:
      current: "category=laboratory&status=final"
notificationShape:
  - resource: Observation
    include: ["Observation:patient"]
```

This is what the matcher will compare against when the publisher sends ORU^R01 messages translated to `Observation` resources.

**Estimated work:** small — write 1–3 demo topics.

### Gap 7 — Subscription filter shape that's demo-friendly — RESOLVED

**Source:** Production-readiness audit B-23

**Status:** RESOLVED via B-23 (`3d80c7d`, cherry-pick of `04e2c36`, merged in `8096936`). The topic catalog now rejects unsupported FHIR search parameters at load time and the matcher fails-closed on the recognised shortlist (`status`, `subject`, `patient`, `code`, `category`, `name`, `_lastUpdated`). A demo using `patient=ABC123` works against the production code path; any unsupported filter is rejected at catalog load rather than silently falling through.

### Gap 8 — Default adapter HL7 → FHIR translation

**Source:** `adapters/default/default.go:143` ("hydrationService is a stub Fetch — vendor adapters must override")

**Current state:** the default reference adapter is a passthrough lexer. To produce a real `Observation` `resource_changes` row from an ORU^R01 message it needs to translate HL7 segments → FHIR fields.

**What the demo needs:** a minimal HL7-to-FHIR translation in the default adapter (or a demo-specific adapter). At minimum:
- ORU^R01 → `Observation` (subject reference, code, value, status)
- ADT^A01 → `Encounter` (subject, period, location)

The translation doesn't need to be spec-perfect — it needs to be good enough that the matcher can see `patient=ABC123` on the resulting resource.

**Estimated work:** medium — write a demo adapter (`adapters/demo/`) that does this minimal translation. Don't pollute `adapters/default/`. Document in the demo README that this is illustrative, not an Epic-grade adapter.

### Gap 9 — Pretty-printable terminal output

**Source:** none — UX

**What the demo needs:** the publisher and subscriber should print colored, structured output so the audience can follow along.

Suggested format:

**Publisher terminal (left):**
```
[14:01:02] → ORU^R01     patient=ABC123 obs=718-7 value=13.5    sent
[14:01:02] ← ACK         control_id=A12345                      OK
[14:01:03] → ORU^R01     patient=XYZ999 obs=718-7 value=12.0    sent
[14:01:03] ← ACK         control_id=A12346                      OK
[14:01:04] → ADT^A01     patient=ABC123 facility="Demo Hosp"    sent
[14:01:04] ← ACK         control_id=A12347                      OK
```

**Subscriber terminal (right):**
```
[14:01:02] ✓ Notification  topic=lab-results  patient=ABC123  obs=718-7   ← match!
[14:01:03]   (no notification — patient=XYZ999 filtered out)
[14:01:04]   (no notification — encounter is wrong topic)
```

The "(no notification)" lines are tricky because the subscriber doesn't know what didn't arrive. Options:
- A **demo mode** where the publisher tells the subscriber out of band what was sent (via a side channel or shared log file) so the subscriber can render absences with strikethrough
- A **wide terminal** running both side-by-side via `tmux` so the operator can correlate visually
- An optional `/$status` poll loop on the subscriber that prints "no events since N" each second

Recommend the tmux approach for simplicity; the side-channel approach for a polished demo.

### Gap 10 — README for the demo path

**Source:** none — UX

**What the demo needs:** `demo/README.md` with:
- One-line description of what the demo shows
- Prereqs (Docker + tmux or two terminals)
- Step-by-step:
  1. `cd demo && docker compose up -d`
  2. In terminal A: `./demo-subscriber --topic http://demo.org/topics/lab-results --filter patient=ABC123`
  3. In terminal B: `./demo-publisher --catalog scenarios/labs.yaml`
  4. Watch the events flow
- A screenshot / animated GIF
- "What just happened" section explaining the pipeline path

**Estimated work:** small — one focused writing session once the demo binaries exist.

---

## Demo flow — end to end

```
┌───────────────────────────┐         ┌─────────────────────────────┐         ┌──────────────────────┐
│  demo-publisher           │ MLLP    │  bridge (cmd/fhir-subs)     │  POST   │  demo-subscriber     │
│  (cmd/demo-publisher)     ├────────>│                             │ ───────>│  (cmd/demo-subscriber)│
│                           │ HL7 v2  │  - MLLP listener            │ rest-   │                      │
│  reads scenarios/labs.yaml│         │  - hl7processor (Stage 1)   │ hook    │  prints received     │
│  prints "→ ORU^R01..."    │         │  - matcher (Stage 2)        │         │  Bundles, color-     │
│  prints "← ACK..."        │<────────┤  - submatcher (Stage 3)     │         │  coded by topic      │
│                           │  ACK    │  - builder (Stage 4)        │         │                      │
└───────────────────────────┘         │  - scheduler (Stage 5)      │         │  POSTed Subscription │
                                      │  - rest-hook channel        │         │  on startup          │
                                      │                             │         │                      │
                                      │  Postgres backing store     │         └──────────────────────┘
                                      └─────────────────────────────┘
```

Per-message flow:

1. Publisher sends `ORU^R01 patient=ABC123` over MLLP.
2. Bridge MLLP listener accepts, persists to `hl7_message_queue`, ACKs.
3. `hl7processor` claims the row, demo adapter translates to `Observation` shape, writes to `resource_changes`, marks queue row processed.
4. `matcher` claims the resource_changes row, evaluates against the lab-results topic, emits `ehr_events` row + outbox.
5. `submatcher` claims the ehr_event, fans out to the demo subscription, writes `deliveries` row.
6. `builder` assembles the Bundle when scheduler picks up the delivery.
7. `scheduler` claims the delivery, dispatches to rest-hook channel.
8. Rest-hook channel POSTs Bundle to subscriber's HTTP endpoint.
9. Subscriber's HTTP handler receives, pretty-prints to stdout.

Per-message-that-shouldn't-match flow:

1. Publisher sends `ORU^R01 patient=XYZ999` over MLLP.
2. Bridge accepts, persists, ACKs (same as above).
3. `hl7processor` translates to `Observation`, writes to `resource_changes`.
4. `matcher` evaluates: topic matches (it's a lab Observation), but the subscription's `filterBy: patient=ABC123` won't match this resource. **No `ehr_events` row is created for this subscription.**
5. **No `deliveries` row, no Bundle, no notification.** Subscriber stays silent.

---

## Effort estimate

If we tackle this in priority order:

| Step | Description | Effort | Blocker for demo? |
|---|---|---|---|
| 1 | ~~Wire API + pipeline workers into `cmd/fhir-subs/run.go`~~ — **DONE** (gaps 1–2 RESOLVED via B-4 wiring `e615c31`) | — | — |
| 2 | Build `cmd/demo-publisher/` (gap 3) | 0.5 day | Yes |
| 3 | Build `cmd/demo-subscriber/` (gap 4) | 0.5 day | Yes |
| 4 | Demo adapter for ORU^R01 → Observation (gap 8) | 1 day | Yes |
| 5 | Demo topic catalog YAML (gap 6) | 0.25 day | Yes |
| 6 | docker-compose.yml for one-command spin-up (gap 5) | 0.5 day | Nice-to-have |
| 7 | Pretty-printed terminal output (gap 9) | 0.5 day | Nice-to-have |
| 8 | demo/README.md with screenshots (gap 10) | 0.5 day | Nice-to-have |

**Total:** roughly **2–3 days of focused work** for a minimum-viable demo (down from 4–5 now that B-4 wiring has landed), **~1 week** for the polished version.

The original biggest dependency — gap 1+2 (production wiring) — is now closed. `cmd/fhir-subs/wiring.go::buildProductionRuntime` mounts the Subscription API on the production binary and starts the pipeline workers, so the demo can target the real binary instead of the e2e harness.

---

## What the demo intentionally does NOT show

To keep scope honest, the demo skips:

- **Auth.** SMART Backend Services token mint + JWKS loop. Use a `--insecure-skip-auth` flag or an in-memory `auth_clients` row with a test issuer for the demo. Document clearly that this is not a production setup.
- **TLS.** MLLP and HTTP both run plaintext for the demo. (Audit B-20 calls this out — production needs TLS; demo doesn't.)
- **Multiple channels.** Only rest-hook. WSS / email / message channels exist on main but the demo binary picks the simplest one.
- **Heartbeats.** Heartbeats are future-work P2.6.
- **Replay.** `$events` operation works on main but the demo doesn't exercise it. Add a stretch goal: kill the subscriber, send 3 messages, restart subscriber, watch it call `$events` to catch up.
- **Multi-subscriber filtering.** Only one subscriber. A natural extension: two subscribers with different filters, see fan-out + filtering simultaneously.

These are great extensions for a v2 of the demo once the foundation works.

---

## Cross-references

- [docs/future-work.md](future-work.md) — P1.1 (adapter framework supervisors), P1.7 (real CapabilityStatement), P1.8 (hydration) all relate but are NOT blockers for the demo as scoped here.
- [docs/production-readiness-audit.md](production-readiness-audit.md) — B-1 through B-4 are the "production wiring" gaps; B-23 is the matcher search-parameter shortlist. The demo can dodge most of the audit findings by sticking to whitelisted filter parameters and acknowledging the auth / TLS shortcuts in the README.
- [docs/architecture.md](architecture.md) — the full pipeline view; the demo is a faithful walk-through of the architecture's "happy path."
- [docs/low-level-design/e2e-harness.md](low-level-design/e2e-harness.md) — the e2e harness already wires what the demo needs. The demo binaries are essentially "shell-friendly wrappers around the harness primitives."

---

## Suggested first PR

A focused first PR could ship gaps 1–5 + 8 in one go: production wiring + publisher CLI + subscriber CLI + demo adapter + demo topic + minimal README. Save the docker-compose, screenshots, and pretty-printing for follow-ups. That would put the demo at "works on a developer's laptop with a one-paragraph cheat sheet" — the right starting point.
