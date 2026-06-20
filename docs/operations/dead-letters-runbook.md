<!-- docs-lint:ignore-cli=dead-letters -->

# Dead-Letter Runbook

The `dead_letters` table accumulates rows whenever the pipeline can't make
progress on a unit of work and no retry will succeed. This runbook covers
inspection, requeue, and forget for an on-call operator.

## What gets dead-lettered

The `dead_letters.kind` column is gated by a CHECK constraint on the
table (see `internal/infra/storage/migrate/migrations/0001_init.sql`).
Only the values below are accepted; the database refuses any other.

| `kind` | Source | Cause | Producer |
|---|---|---|---|
| `hl7_unparseable` | `hl7_message_queue` | Default mapping for unrecoverable HL7 ingest failures: MSH parse, missing required fields, transaction-begin failures past `MaxRowAttempts`, anything `dlKindForClass` does not classify as validation. | `internal/hl7processor/processor.go::dlKindForClass` |
| `hl7_invalid_fhir` | `hl7_message_queue` | The HL7 parsed but resource emission produced an invalid FHIR resource. | `internal/hl7processor/processor.go::dlKindForClass` (`ErrorClassValidation`) |
| `delivery_exhausted` | `deliveries` | The scheduler exhausted retries on a channel send (transient or permanent), or the channel's activation pre-check decided the row was dead. | `internal/engine/scheduler/worker.go` (three call sites — terminal-failure, exhausted-retries, panic-recovery) |
| `channel_permanent_failure` | `deliveries` | Reserved by the schema; no production code path emits this kind today. The CHECK constraint accepts it for forward compatibility with a planned channel-side terminal classifier. |

The Kind on `dead_letters` is the same string that appears as the
`reason` label on the `fhir_subs_dead_letters_total` counter
(`internal/infra/observability/metrics/metrics.go::DeadLettersTotal`,
emitted from the reporter installed in `observability.Start` via
`repos.SetDeadLetterReporter`).

## Alerting

Alert on:

```
sum by (reason) (rate(fhir_subs_dead_letters_total[5m])) > 0.1
```

Operators should set rule severity by `reason`:

- `hl7_unparseable` — usually a benign upstream blip (vendor formatting,
  one-off truncation). Page only on a sustained rate.
- `hl7_invalid_fhir` — a parser/adapter regression. Page on first
  occurrence after a deploy.
- `delivery_exhausted` — subscriber-impacting. Page on first occurrence
  per `reason` per subscription (cross-reference the
  `fhir_subs_api_subscription_created_total`,
  `fhir_subs_api_subscription_updated_total`, and
  `fhir_subs_api_subscription_deleted_total` counters from
  `internal/api/metrics/metrics.go` to identify the subscription that
  owns the row).

## Inspect

```sql
SELECT id, kind, source_table, source_id, subscription_id,
       reason, error_detail, correlation_id, created_at
FROM dead_letters
WHERE created_at > now() - interval '1 hour'
ORDER BY created_at DESC
LIMIT 50;
```

`payload_redacted` is encrypted at rest with the application codec
key version active at insert time
(`internal/infra/storage/repos/dead_letters.go`). Decryption is not
part of the routine inspection path; reach for it only when
correlating an incident, and route the decryption through the audit
log so the lookup itself is logged.

## Pre-recovery checks (READ BEFORE REQUEUEING)

A naive requeue can loop forever or land on a permanently-broken
channel. Before any requeue, confirm:

1. **Confirm the channel type is actually wired.** The scheduler
   activates channels via
   `cmd/fhir-subs/wiring.go::handlers.ChannelRegistry`. Today the
   production registry installs activators for `rest-hook`,
   `websocket`, `message`, and `email`. A subscription whose
   `channelType` is not in this map will dead-letter on every retry.
   Inspect the related subscription:

   ```sql
   SELECT id, status, channel_type FROM subscriptions
   WHERE id = (SELECT subscription_id FROM dead_letters WHERE id = '<dead_letter_id>');
   ```

   If `channel_type` is unrecognized, **do not** requeue. Either
   delete the subscription or wait for a release that registers the
   channel.

2. **Confirm the email channel is configured if the subscription is
   email.** The email activator is fail-closed when SMTP is not
   configured (`unconfiguredEmailActivator` in `wiring.go`). A
   requeue against an unconfigured email setup is a guaranteed
   re-dead-letter.

3. **Confirm the source row still exists.** The retention sweeper
   may have already swept the upstream row out. Check
   `dead_letters.source_table` and `source_id`:

   ```sql
   -- example for a delivery_exhausted row
   SELECT EXISTS (SELECT 1 FROM deliveries WHERE id = '<source_id>');
   ```

## Requeue

There is no admin CLI in v1 (deferred — see future-work P1.6). Manual
requeue procedure:

### `hl7_unparseable` / `hl7_invalid_fhir`

These rows exist because the message can't be processed in its
current form. Requeueing without a fix is a no-op. The flow is:

1. Identify the upstream defect (often an adapter version mismatch
   or a vendor-side malformed message).
2. Fix at the source (vendor or adapter).
3. If the message is a once-off and the operator wants the system to
   try again, re-insert it into `hl7_message_queue` with a fresh
   `id`:

   ```sql
   INSERT INTO hl7_message_queue (id, adapter_id, payload, received_at, status)
   SELECT gen_random_uuid(), adapter_id, payload, now(), 'queued'
   FROM hl7_message_queue
   WHERE id = '<source_id_from_dead_letter>';
   ```

   The original `payload` is encrypted at rest; a `SELECT` returns
   the encrypted blob, which the processor will decrypt on the next
   claim using the codec the row was inserted under.

### `delivery_exhausted`

The subscriber's endpoint failed up to `RetryConfig.MaxAttempts`, or
the scheduler's panic-recovery / build-error path classified the row
as terminal. Look at the most recent `deliveries.last_error` and
`attempt_count`. Common causes:

- Subscriber's auth token expired — they must rotate then ask the
  operator to re-trigger.
- Subscriber endpoint is permanently gone — confirm with the client
  owner; either delete the subscription or change the endpoint.
- Transient network failure that exceeded the retry window —
  re-deliver by inserting a fresh `deliveries` row pointing to the
  same `ehr_event_id` (after running the pre-recovery checks above):

  ```sql
  INSERT INTO deliveries (id, ehr_event_id, subscription_id, attempt_count, status, scheduled_at)
  SELECT gen_random_uuid(), ehr_event_id, subscription_id, 0, 'pending', now()
  FROM deliveries
  WHERE id = '<source_id_from_dead_letter>';
  ```

### `channel_permanent_failure`

Not produced by current code. If a row appears with this kind, the
operator is on a future release that wires a channel-side terminal
classifier — consult the release's runbook addendum before requeueing.

## Forget

Once an operator has determined a row is permanently un-recoverable
(subscriber gone, message superseded, etc.), the row stays in
`dead_letters` for audit. Retention policy:

- The retention sweeper sweeps `dead_letters` according to
  `storage.retention.dead_letters` (the operator-supplied YAML key
  at `cmd/fhir-subs/config.go::StorageRetentionConfig.DeadLetters`).
  Defaults are filled in by `storage.Config.ApplyDefaults` —
  inspect the rendered config with `fhir-subs --check-config`.
- To force-forget earlier, simply
  `DELETE FROM dead_letters WHERE id = '<id>'`. The table has no
  foreign-key dependents.
- Always document the forget in the on-call log so the audit trail
  is preserved (the audit chain does NOT capture dead-letter
  deletions).

## On-call quick reference

```sh
# Top-N reasons in last hour, sorted by count
psql -c "SELECT kind, COUNT(*) FROM dead_letters
         WHERE created_at > now() - interval '1 hour'
         GROUP BY kind ORDER BY 2 DESC;"

# Sample per-reason detail
psql -c "SELECT DISTINCT ON (kind) kind, source_id, reason, error_detail, created_at
         FROM dead_letters
         WHERE created_at > now() - interval '1 hour'
         ORDER BY kind, created_at DESC;"

# Watch the metric. /metrics is mounted on the FHIR API listener
# (server.http.bind), not on a separate port — see
# cmd/fhir-subs/wiring.go: r.Handle("/metrics", ...).
curl -s "http://${FHIR_SUBS_HOST}:${FHIR_SUBS_PORT}/metrics" | grep fhir_subs_dead_letters_total
```

## Future improvements

A `fhir-subs dead-letters list|replay|forget` admin CLI is deferred
(see [future-work.md](../future-work.md) P1.6). Until then, the SQL
above is the operator surface.
