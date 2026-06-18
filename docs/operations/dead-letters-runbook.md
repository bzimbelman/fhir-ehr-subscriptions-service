# Dead-Letter Runbook

The `dead_letters` table accumulates rows whenever the pipeline can't make progress on a unit of work and no retry will succeed. This runbook covers inspection, requeue, and forget for an on-call operator.

## What gets dead-lettered

| `kind` (label `reason`) | Source | Cause |
|---|---|---|
| `hl7_unparseable` | `hl7_message_queue` | The MSH segment failed to parse; no resource emission was possible |
| `hl7_unsupported` | `hl7_message_queue` | The message type / event is not in the adapter's contributed event map |
| `hl7_invalid` | `hl7_message_queue` | A required field (e.g., MSH-10 control id) was missing |
| `rolled_back` | `hl7_message_queue` | The processor's transaction errored repeatedly past `MaxRowAttempts` |
| `delivery_exhausted` | `deliveries` | The scheduler retried the channel up to `RetryConfig.MaxAttempts` |
| `poison_row` | `resource_changes` | The matcher transaction errored past `MaxRowAttempts` |

The Kind on `dead_letters` is the same string that appears as the `reason` label on the `fhir_subs_dead_letters_total` counter.

## Alerting

Alert on:

```
rate(fhir_subs_dead_letters_total[5m]) > 0.1
```

per `reason`. Operators should set rule severity by reason — `hl7_unparseable` is usually a benign upstream blip, `delivery_exhausted` and `poison_row` are subscriber-impacting.

## Inspect

```sql
SELECT id, kind, source_table, source_id, subscription_id,
       reason, error_detail, correlation_id, created_at
FROM dead_letters
WHERE created_at > now() - interval '1 hour'
ORDER BY created_at DESC
LIMIT 50;
```

`payload_redacted` is encrypted at rest with the AEAD key version active at insert time. Decryption is not part of the routine inspection path; reach for it only when correlating an incident.

## Requeue

There is no admin CLI in v1 (deferred — see future-work P1.6). Manual requeue procedure:

### `hl7_unparseable` / `hl7_unsupported` / `hl7_invalid`

These rows exist because the message can't be processed in its current form. Requeueing without a fix is a no-op. The flow is:

1. Identify the upstream defect (often an adapter version mismatch or a vendor-side malformed message)
2. Fix at the source (vendor, adapter)
3. If the message is a once-off and the operator wants the system to try again, re-insert it into `hl7_message_queue` with a fresh `id`:

   ```sql
   INSERT INTO hl7_message_queue (id, adapter_id, payload, received_at, status)
   SELECT gen_random_uuid(), <adapter_id>, payload, now(), 'queued'
   FROM hl7_message_queue
   WHERE id = <source_id_from_dead_letter>;
   ```

   The original `payload` is encrypted at rest; a `SELECT` returns the encrypted blob, which the processor will decrypt on the next claim.

### `rolled_back` / `poison_row`

These exhausted retries because the row triggered a panic or persistent error. Requeueing without a fix loops forever. The flow is:

1. Reproduce locally if possible (`payload_redacted` carries the offending body redacted)
2. Fix the bug
3. Deploy the fix
4. Manually un-mark the source row's processed/locked state (the schema doesn't expose this through SQL — open an incident)

### `delivery_exhausted`

The subscriber's endpoint failed up to `RetryConfig.MaxAttempts`. Look at the most recent `deliveries.last_error` and `attempt_count`. Common causes:

- Subscriber's auth token expired — they must rotate then ask the operator to re-trigger
- Subscriber endpoint is permanently gone — confirm with the client owner; either delete the subscription or change the endpoint
- Transient network failure that exceeded the retry window — re-deliver by inserting a fresh `deliveries` row pointing to the same `ehr_event_id`:

  ```sql
  INSERT INTO deliveries (id, ehr_event_id, subscription_id, attempt_count, status, scheduled_at)
  SELECT gen_random_uuid(), ehr_event_id, subscription_id, 0, 'pending', now()
  FROM deliveries
  WHERE id = <source_id_from_dead_letter>;
  ```

## Forget

Once an operator has determined a row is permanently un-recoverable (subscriber gone, message superseded, etc.), the row stays in `dead_letters` for audit. Retention policy:

- `dead_letters` is swept by the retention job at `Pipeline.Retention.DeadLetters` (default: 90 days)
- To force-forget earlier, simply `DELETE FROM dead_letters WHERE id = <id>`. The table has no foreign-key dependents
- Always document the forget in the on-call log so the audit trail is preserved

## On-call quick reference

```sh
# Top-N reasons in last hour, sorted by count
psql -c "SELECT kind, COUNT(*) FROM dead_letters WHERE created_at > now() - interval '1 hour' GROUP BY kind ORDER BY 2 DESC;"

# Sample 10 rows per reason
psql -c "SELECT DISTINCT ON (kind) kind, source_id, reason, error_detail, created_at FROM dead_letters WHERE created_at > now() - interval '1 hour' ORDER BY kind, created_at DESC;"

# Watch the metric
curl -s http://<host>:9090/metrics | grep fhir_subs_dead_letters_total
```

## Future improvements

A `fhir-subs dead-letters list|replay|forget` admin CLI is deferred (see [future-work.md](../future-work.md) P1.6). Until then, the SQL above is the operator surface.
