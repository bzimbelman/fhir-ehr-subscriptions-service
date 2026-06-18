-- 0006_hl7_message_queue_attempt_count.sql
--
-- S-9.9: Per-row retry budget for the hl7processor BeginTx failure
-- path. Without this column a poison row (or a row whose tx-begin
-- attempt keeps tripping a pgxpool error) pins the worker forever:
-- the claim loop re-peeks it, BeginTx fails, the row stays
-- processed=false, repeat. We mirror the matcher / submatcher
-- MaxRowAttempts knob (S-10.6 / S-12) by tracking attempts per row
-- and dead-lettering once the budget is exhausted.
--
-- Default 0 — every existing row starts with a fresh budget on the
-- next claim cycle. The column is bumped via a separate, short-lived
-- statement (not the failed transaction) so a transient pool error
-- does not also block the increment.
--
-- A non-CONCURRENTLY ALTER TABLE takes an AccessExclusive lock briefly
-- while the catalog updates; the column has a constant default so
-- Postgres ≥ 11 fast-paths the rewrite.

alter table hl7_message_queue
    add column if not exists attempt_count int not null default 0;

comment on column hl7_message_queue.attempt_count is
    'S-9.9 per-row retry budget — incremented on BeginTx failure, capped by hl7processor.Config.MaxRowAttempts.';
