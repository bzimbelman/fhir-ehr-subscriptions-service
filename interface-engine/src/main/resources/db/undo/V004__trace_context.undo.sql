-- V004 downgrade (Epic #387, ticket #394).
--
-- Companion to V003__correlation_id.undo.sql — Flyway OSS doesn't auto-run
-- .undo files; this is runnable documentation an operator can `psql -f` to
-- back out the trace_context column on a non-prod environment.

ALTER TABLE ingested_messages
  DROP COLUMN IF EXISTS trace_context;
