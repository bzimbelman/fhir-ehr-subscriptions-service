-- migrations/0008_ehr_events_client_id.sql
--
-- OP #272 + #274: per-client fan-out for ehr_events.
--
-- Background: pre-#272 ehr_events stored one row per (resource_change ×
-- topic) with no client_id. Two tenants subscribed to the same topic
-- share an event log; the $events handler's owner check guards the
-- subscription row but not the event rows, leaking PHI across tenants
-- (OP #197).
--
-- The fan-out rule, implemented in internal/matcher/matcher.go, emits
-- one ehr_events row per (resource_change × topic × subscription.client_id)
-- so each row carries the recipient tenant. The $events SQL adds the
-- client_id predicate so a given caller only sees their own rows.
--
-- This service is greenfield (no production tenants) so we collapse the
-- expand/contract dance into a single forward-only migration:
--
--   * Add client_id text NOT NULL with FK to auth_clients(id).
--   * No backfill of existing rows: any pre-migration ehr_events rows
--     would be tenant-orphaned and cannot be safely fanned out
--     post-hoc; truncate rather than guess. The deliveries table
--     references ehr_events by id — truncate it too so the FK chain
--     stays consistent.
--
-- Forward-only. If a deployment somewhere actually has tenant data,
-- DO NOT apply this migration; instead split into expand (nullable
-- client_id), backfill from the deliveries → subscriptions chain, then
-- contract to NOT NULL.

-- The ehr_events table is partitioned monthly by created_month. ALTER
-- TABLE … ADD COLUMN against the parent cascades to every attached
-- partition automatically.
truncate table deliveries;
truncate table ehr_events;

alter table ehr_events
    add column client_id text not null
        references auth_clients(id);

create index ehr_events_topic_client_event_idx
    on ehr_events (topic_url, client_id, event_number);

comment on column ehr_events.client_id is
    'Recipient tenant for this event row. The matcher emits one row per (resource_change × topic × subscription.client_id) so $events reads filter by the caller''s client_id without cross-tenant leakage. References auth_clients(id).';
