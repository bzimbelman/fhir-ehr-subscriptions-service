-- V002 downgrade (Epic #378, ticket #380).
--
-- Flyway OSS does NOT execute .undo.sql automatically (that's a Teams /
-- Enterprise feature). Worse, if this file lived under `db/migration/`
-- Flyway OSS would still RESOLVE it as a migration and fail validation
-- with "more than one migration with version 002". So we park it in a
-- sibling directory (`db/undo/`) that is NOT on `spring.flyway.locations`.
--
-- This file is kept as runnable documentation: operators can
-- `psql -f V002__ingested_messages.undo.sql` to back out the migration
-- on a non-production environment, and it proves the forward migration
-- is reversible.
--
-- Order matters: drop the table first (it depends on the enum types),
-- then the enum types.

DROP INDEX IF EXISTS ix_ingested_received_at;
DROP INDEX IF EXISTS ix_ingested_next_attempt;
DROP INDEX IF EXISTS ix_ingested_status_received;

DROP TABLE IF EXISTS ingested_messages;

DROP TYPE IF EXISTS ingested_message_source_protocol;
DROP TYPE IF EXISTS ingested_message_status;
