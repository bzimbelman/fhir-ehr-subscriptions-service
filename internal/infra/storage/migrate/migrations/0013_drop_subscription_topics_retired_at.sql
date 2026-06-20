-- 0013_drop_subscription_topics_retired_at.sql
--
-- OP #142: subscription_topics.retired_at is read by the repo but never
-- written by any production code path; the constraint
--     status in ('draft', 'active', 'retired')
-- carries a 'retired' value that no caller can produce. Drop the
-- column and tighten the constraint rather than wire up a RetireTopic
-- repo + admin handler that nobody has asked for. A future story that
-- actually needs retirement can re-add both under expand/contract.

alter table subscription_topics drop column if exists retired_at;

alter table subscription_topics drop constraint if exists subscription_topics_status_check;

alter table subscription_topics
    add constraint subscription_topics_status_check
    check (status in ('draft', 'active'));
