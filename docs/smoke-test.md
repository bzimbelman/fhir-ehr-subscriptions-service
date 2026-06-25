# End-to-end smoke test

How to verify the stack is healthy after a fresh `docker-compose up` (or a deploy to the-deploy-host). Each step is independent — if step N fails, you only need to debug N and re-run downstream steps.

Variables used below:

| Var | Default | What it is |
|---|---|---|
| `BASE_URL` | `https://subscription-service.bzonfhir.com` | Public FHIR endpoint |
| `BASE_LOCAL` | `http://localhost:18080` | HAPI on the docker host (the-deploy-host or laptop) |
| `MLLP_HOST` | `localhost` (run on the-deploy-host) | IPF MLLP listener host |
| `MLLP_PORT` | `2575` | IPF MLLP listener port |

## 1. CapabilityStatement

```bash
curl -fsS $BASE_URL/fhir/metadata | jq '{fhirVersion, software}'
```

**Expect**: `fhirVersion=4.0.1`, `software.name="HAPI FHIR Server"`.

## 2. US Core 7.0 profile loaded

```bash
curl -fsS "$BASE_URL/fhir/StructureDefinition?url=http://hl7.org/fhir/us/core/StructureDefinition/us-core-patient" | jq '.total'
```

**Expect**: `1`.

## 3. Subscriptions Backport IG resources

```bash
curl -fsS "$BASE_URL/fhir/StructureDefinition?url=http://hl7.org/fhir/uv/subscriptions-backport/StructureDefinition/backport-subscription" | jq '.total'
```

**Expect**: `1`.

Note on `SubscriptionTopic` resources: the Backport IG ships `StructureDefinition`s and `OperationDefinition`s, not example `SubscriptionTopic` instance data. Topics are created by the service operator (or consumers) — `GET /fhir/SubscriptionTopic` returning `total=0` on a fresh deployment is normal.

## 4. Write a Patient

```bash
curl -fsS -X POST -H 'Content-Type: application/fhir+json' \
  -d '{"resourceType":"Patient","name":[{"family":"Smoke","given":["Test"]}]}' \
  $BASE_URL/fhir/Patient | jq '{id, name}'
```

**Expect**: an `id` field comes back. Posting again creates a second resource (default HAPI behavior).

## 5. MLLP listener accepts an ADT message

This step runs on the deploy host (the-deploy-host or laptop running compose), not over the public hostname (Cloudflare's tunnel is HTTP-only — MLLP is LAN-only by design).

```bash
printf '\x0bMSH|^~\\&|EPIC|HOSP|RECEIVER|CDS|20260625120000||ADT^A04|SMOKE001|P|2.5\rEVN|A04|20260625120000\rPID|1||MRN-SMOKE^^^HOSP^MR||TEST^E2E^Q||19800101|M\rPV1|1|I|2000^2012^01\r\x1c\r' \
  | nc -w 3 $MLLP_HOST $MLLP_PORT
```

**Expect**: an `MSH|...|ACK^A04^ACK|...` ACK message ending in `MSA|AA|SMOKE001`. The IPF app log will show:

```
passthrough type=ADT_A04 controlId=SMOKE001 sendingApp=EPIC (no transform configured)
```

ADT^A04 is intentionally the demo message — it routes through the *pass-through* branch and doesn't try to call Matchbox. ADT^A01 *does* call Matchbox; until the v2-to-FHIR StructureMaps land (a separate ticket), ADT^A01 returns `MSA|AE|...` by design.

## 6. End-to-end Subscription notification

Once `SubscriptionTopic` and example `StructureMap` work is done, the full e2e looks like:

1. `POST /fhir/Subscription` with `channel.type=rest-hook` pointing at `webhook.site`.
2. Send an `ADT^A01` to the MLLP listener.
3. Within seconds, the webhook receives a notification referencing the new `Patient`.

This is the goal state — covered by a separate end-to-end test ticket, not part of #362 acceptance.

## Common failure modes

- **HAPI returns 200 on `/fhir/metadata` but writes hang** — Postgres bind mount unwritable. Check `POSTGRES_DATA_DIR` perms.
- **HAPI crashloops with `HAPI-2031: Error loading file:///app/igs/...`** — IG tarball is mode 0600 and the HAPI container user can't read it. Run `chmod 644 hapi/igs/*.tgz` (the `scripts/fetch-igs.sh` script does this automatically since 2026-06-25).
- **Public hostname returns 404 from Cloudflare but `localhost:18080` works** — `cloudflared` has multiple instances and one of them is on a stale config. Verify with `ps -ef | grep cloudflared` and restart any pre-tunnel-change processes.
- **MLLP listener accepts but ACK is AE** — the IPF app tried to call Matchbox or POST to HAPI and got an error. Check `docker logs subscription-service-ipf-app` for the `stage=matchbox|hapi reason=...` line.
