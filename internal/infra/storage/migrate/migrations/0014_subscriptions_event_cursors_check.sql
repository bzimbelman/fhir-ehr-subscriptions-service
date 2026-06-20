-- 0014_subscriptions_event_cursors_check.sql
--
-- OP #144: subscriptions tracks per-subscription event progress with two
-- columns:
--
--   - next_event_number is the cursor the submatcher advances under
--     SELECT FOR UPDATE at fanout time (migration 0004). It is the
--     monotonic source of event_number values written into deliveries
--     and ehr_events.
--
--   - events_since_subscription_start is the wire-visible counter the
--     matcher folds in batches once events are recorded for delivery
--     (story #56 / S-12.4). Subscribers see it on the
--     SubscriptionStatus heartbeat and use it to detect missed events.
--
-- The invariant is that events_since_subscription_start can never run
-- ahead of next_event_number — a number must be issued before a
-- subscriber can see it. The two columns are written from independent
-- code paths, so a code regression that bumps the wire counter without
-- first advancing the issuance cursor would silently produce an
-- impossible state. The CHECK constraint here makes that regression a
-- write-time error instead of a wire-time mystery.
--
-- See docs/architecture.md "Subscription event cursors" for the
-- complementary documentation.

alter table subscriptions drop constraint if exists subscriptions_event_cursors_check;

alter table subscriptions
    add constraint subscriptions_event_cursors_check
    check (next_event_number >= events_since_subscription_start);

comment on constraint subscriptions_event_cursors_check on subscriptions is
    'OP #144: events_since_subscription_start (wire-visible) must never run ahead of next_event_number (the per-subscription issuance cursor). Independent write paths converge on this invariant.';
