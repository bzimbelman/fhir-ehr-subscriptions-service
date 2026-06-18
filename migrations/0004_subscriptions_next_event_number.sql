-- 0004_subscriptions_next_event_number.sql
--
-- Persists the per-subscription monotonic event_number cursor on the
-- subscriptions row itself. Eliminates two correctness bugs at once:
--
--   B-26: Two concurrent submatcher workers on the same subscription
--         could both compute MAX(deliveries.event_number)+1 and try to
--         INSERT the same number. The UNIQUE on
--         (subscription_id, event_number) catches it but the worker
--         abandons the whole tick instead of retrying. With
--         next_event_number on subscriptions we take row-level
--         SELECT FOR UPDATE before issuing each number, so contention
--         serializes naturally.
--
--   B-27: Retention can DELETE old deliveries rows. Once that happens
--         MAX(deliveries.event_number) drops, and the next fanout
--         re-uses a number a delivered subscriber has already seen.
--         Subscribers track the wire-visible
--         events_since_subscription_start, so a re-used low number
--         silently breaks the WSS replay-from-cursor contract. With
--         the cursor on subscriptions, retention is free to delete
--         deliveries without affecting numbering.
--
-- Forward-compatible: the column is added NOT NULL with default 0; the
-- backfill UPDATE seeds it from existing deliveries so no in-flight
-- subscription regresses.

alter table subscriptions
    add column if not exists next_event_number bigint not null default 0;

-- Backfill: seed next_event_number from the highest event_number a
-- subscription has ever produced. NULL → 0 via COALESCE.
update subscriptions s
    set next_event_number = greatest(
        s.next_event_number,
        coalesce((select max(d.event_number) from deliveries d where d.subscription_id = s.id), 0)
    );

comment on column subscriptions.next_event_number is 'Per-subscription monotonic event_number cursor; advanced under SELECT FOR UPDATE at fanout time.';
