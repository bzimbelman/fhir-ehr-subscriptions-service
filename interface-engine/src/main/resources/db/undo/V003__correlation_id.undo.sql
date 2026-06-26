-- V003 downgrade (Epic #387, ticket #388).
--
-- Flyway OSS doesn't auto-execute .undo.sql; this file lives in the
-- sibling `db/undo/` directory (not on `spring.flyway.locations`) as
-- runnable documentation. An operator can `psql -f V003__correlation_id.undo.sql`
-- to back out the correlation_id column on a non-production environment.

DROP INDEX IF EXISTS ix_ingested_correlation_id;

ALTER TABLE ingested_messages
  DROP COLUMN IF EXISTS correlation_id;
