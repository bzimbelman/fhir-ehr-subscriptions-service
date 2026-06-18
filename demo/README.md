# Subscription sidecar demo

A self-contained walkthrough of the bridge's happy path: a `demo-publisher`
sends HL7 v2 messages over MLLP, the bridge translates and matches them
against a registered FHIR Subscription, and a `demo-subscriber` receives the
notification Bundles for the messages that pass topic + filter checks.

The demo is intentionally narrow. It exercises one adapter (HL7 v2 ORU/ADT),
one channel (rest-hook), and one topic (`lab-results`) so that the pipeline
stages — MLLP listener → `hl7processor` → `matcher` → `submatcher` →
`builder` → `scheduler` → rest-hook delivery — are visible end-to-end.

The full design lives in [`../docs/subscription-sidecar-demo.md`](../docs/subscription-sidecar-demo.md);
this README is the operator's cheat sheet.

## What the demo intentionally skips

These are documented in the design doc and called out here so nobody is
surprised when they peek at `config.yaml`:

- **Auth.** No SMART Backend Services token mint, no JWKS. The bridge runs
  with no audience configured, which mounts the no-op auth middleware.
- **TLS.** MLLP and HTTP both run plaintext.
- **Multiple channels.** Only rest-hook. WSS / email / message channels exist
  on `main` but the demo picks the simplest one.
- **Heartbeats / replay / multi-subscriber fan-out.** Future work; not
  needed to see the pipeline move.

## Prerequisites

- Docker 24+ with `docker compose` v2
- Go 1.22+ (only needed if you want to rebuild `demo-publisher` /
  `demo-subscriber` from source instead of using the binaries baked into the
  compose image)
- Two terminals, or `tmux`. The publisher and subscriber each run in the
  foreground and stream output as messages flow.

A `curl` for poking at `/Subscription` and `jq` for pretty-printing payloads
are nice-to-haves but not required.

## Spin up the bridge

From the repository root:

```sh
cd demo
docker compose up -d
```

The compose stack brings up:

- `postgres` — backs the bridge's storage (resource_changes, ehr_events,
  deliveries, audit, …). No data is persisted across `docker compose down -v`.
- `bridge` — `cmd/fhir-subs` configured by `demo/config.yaml`. Listens on
  HTTP `:8443` for the Subscription API and on MLLP `:2575` for HL7 v2
  ingest. Both are exposed on the host. The bridge loads its
  `SubscriptionTopic` catalog from [`topics/`](topics/) (`lab-results`,
  `vitals`, `encounter-admit`).

Wait for the bridge to report `/readyz` OK:

```sh
curl -fsS http://localhost:8443/readyz
```

> **Note.** If `demo/docker-compose.yml` is not present in your checkout
> yet, story #82 owns landing it. Until then you can run the bridge directly
> with `go run ./cmd/fhir-subs --config demo/config.yaml` and a local
> Postgres pointed at by `demo/config.yaml`'s `database.url`. Everything
> after this point is identical.

## Walk-through

### Terminal A — start the subscriber

```sh
./demo-subscriber \
  --bridge http://localhost:8443 \
  --topic http://demo.org/topics/lab-results \
  --filter patient=ABC123 \
  --token demo-token
```

`demo-subscriber`:

1. Brings up a local HTTP listener that the bridge will POST notification
   Bundles to.
2. POSTs a rest-hook `Subscription` to the bridge with the topic + filter
   from the flags and `endpoint` set to its own listener URL.
3. Pretty-prints each Bundle the bridge delivers, color-coded by topic.

It logs the subscription `id`, the `endpoint` it advertised, and the bearer
token it used so the bridge ↔ subscriber side of the handshake is auditable
from the terminal.

The full flag list lives in `cmd/demo-subscriber/main.go`; the most useful
ones are `--listen` (override the bind address), `--advertise` (override the
URL the bridge POSTs to — useful when listening on `0.0.0.0` inside Docker),
and the JWT-mint flags (`--client-id`, `--private-key`, `--kid`) for
exercising the SMART backend services flow.

### Terminal B — run the publisher

```sh
./demo-publisher \
  --addr 127.0.0.1:2575 \
  --catalog scenarios/labs.yaml
```

`demo-publisher` walks the catalog top-to-bottom, dialing the bridge's MLLP
listener for each entry, framing one HL7 v2 message, and printing a
`→ send` line followed by an `← ACK` line per message. Delays between
sends are taken from the `delay:` field on each catalog entry.

The bundled `scenarios/labs.yaml` has four messages:

| # | Template  | Patient  | Expected outcome                                        |
|---|-----------|----------|---------------------------------------------------------|
| 1 | `oru-r01` | `ABC123` | **Match.** Subscriber prints a Bundle.                  |
| 2 | `oru-r01` | `XYZ999` | Filtered out — wrong patient. Subscriber stays silent.  |
| 3 | `adt-a01` | `ABC123` | Filtered out — wrong topic. Subscriber stays silent.    |
| 4 | `oru-r01` | `ABC123` | **Match.** Subscriber prints a Bundle.                  |

The whole script takes about ten seconds. Two of the four sends should
trigger notification Bundles; the other two should not.

## What to expect

A polished demo includes screenshots / GIFs that capture the two terminals
side-by-side. Until those are recorded the placeholders below mark where
they go:

- `./screenshots/publisher.png` — `demo-publisher` mid-run, showing the
  `→ send` / `← ACK` pairs and the colored summary column.
- `./screenshots/subscriber.png` — `demo-subscriber` printing two received
  Bundles with the third and fourth catalog entries notably absent.
- `./screenshots/demo.gif` — animated capture of both terminals via `tmux`,
  with publisher on the left and subscriber on the right.

> Image files are not committed yet. Recording them is tracked alongside
> this README; replace the placeholders when assets land under
> `demo/screenshots/`.

## What just happened

For each `oru-r01 patient=ABC123` send the pipeline runs end-to-end:

1. `demo-publisher` writes one MLLP-framed HL7 v2 message to the bridge.
2. The bridge's **MLLP listener** persists the raw frame to
   `hl7_message_queue` and ACKs the publisher.
3. **`hl7processor`** claims the queue row, runs the demo adapter
   (`adapters/demo`) to translate HL7 → FHIR `Observation`, and writes a
   `resource_changes` row.
4. **`matcher`** evaluates the change against the topic catalog. The
   `lab-results` topic matches `Observation`, so a `ehr_events` row plus an
   outbox row are written.
5. **`submatcher`** fans the event out to the demo subscription. The
   subscription's `filterBy: patient=ABC123` is checked against the
   resource; matches produce a `deliveries` row.
6. **`builder`** assembles the notification Bundle for the delivery.
7. **`scheduler`** claims the delivery and dispatches it to the rest-hook
   channel.
8. The **rest-hook channel** POSTs the Bundle to the subscriber's HTTP
   endpoint.
9. `demo-subscriber` receives the POST and pretty-prints the Bundle.

For the `XYZ999` send the pipeline still runs through stage 4 — the
`Observation` is persisted and the topic still matches — but the
subscription's filter rejects it at the submatcher, so no `deliveries` row
is created and the subscriber sees nothing. For the `adt-a01` send the
matcher rejects the change at stage 4 (encounter ↛ lab-results topic) and
the pipeline stops there.

The diagram in
[`../docs/subscription-sidecar-demo.md`](../docs/subscription-sidecar-demo.md#demo-flow--end-to-end)
walks each stage in more detail, including which tables get written.

## Troubleshooting

**The subscriber prints `subscribe: ... 401`.** The bridge thinks auth is
on. Confirm `demo/config.yaml` has no `audience:` set — that's what causes
the no-op middleware to mount.

**The subscriber prints `listen 127.0.0.1:0: bind: address already in use`.**
Pass `--listen 127.0.0.1:9090` (or any free port) instead of relying on the
default ephemeral bind.

**The publisher prints `dial tcp 127.0.0.1:2575: connect: connection
refused`.** The bridge's MLLP listener is not up yet. `docker compose ps`
should show `bridge` healthy; if it's still booting, wait a few seconds and
retry. If the listener never comes up, check `docker compose logs bridge`
for `mllp.listener.error` lines.

**The publisher gets `← ACK` for every message but the subscriber never
prints a Bundle.** The HL7 made it into the bridge but didn't reach the
subscription. Most common causes:

- The subscription was created against a different topic URL than the
  catalog. The publisher's catalog targets `http://demo.org/topics/lab-results`;
  the subscriber must use the same `--topic`.
- The bridge couldn't POST back to the advertised endpoint. If you launched
  the subscriber inside Docker, pass `--advertise http://host.docker.internal:<port>/`
  so the bridge dials the host's loopback.
- Postgres is wedged. `docker compose logs postgres` and
  `docker compose exec postgres psql -U fhirsubs -c 'select count(*) from
  resource_changes;'` will tell you whether stage 3 ran.

**`docker compose up` errors with a port collision on 2575 / 8443 / 5432.**
Another process on the host owns the port. Either stop it or override the
port mapping in `demo/docker-compose.yml`.

## Pointers

- [`../docs/subscription-sidecar-demo.md`](../docs/subscription-sidecar-demo.md) — the design doc this demo implements, including the gap inventory.
- [`../docs/architecture.md`](../docs/architecture.md) — the full pipeline view; the demo is a faithful walk of its happy path.
- [`../docs/low-level-design/e2e-harness.md`](../docs/low-level-design/e2e-harness.md) — the e2e harness shares primitives with the demo binaries.
- [`scenarios/labs.yaml`](scenarios/labs.yaml) — the bundled catalog; copy and tweak for new scenarios.
- [`topics/`](topics/) — the demo `SubscriptionTopic` catalog the bridge loads at startup.
- [`../cmd/demo-publisher`](../cmd/demo-publisher) and [`../cmd/demo-subscriber`](../cmd/demo-subscriber) — source for the two CLIs.
