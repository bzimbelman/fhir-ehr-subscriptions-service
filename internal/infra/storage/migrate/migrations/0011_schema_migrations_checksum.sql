-- 0011_schema_migrations_checksum.sql
--
-- OP #140: the original migrate runner ensured the schema_migrations
-- table had a checksum column by running an inline
--
--     ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT
--
-- on every apply pass, with its error suppressed (audit B-32). That
-- ALTER is bootstrap-after-the-fact — it only matters on databases that
-- applied 0001_init.sql before the runner ever wrote a checksum — and
-- it sits outside the version-tracked migration sequence so its DDL is
-- invisible to anyone reading the migrations directory.
--
-- This migration moves that ALTER into the numbered sequence. It is a
-- no-op on greenfield databases that already have the column (the
-- previous runner created it inline before this migration ever ran),
-- and it is the canonical bootstrap step for older deployments. The
-- runner no longer issues the inline ALTER.
--
-- IF NOT EXISTS keeps it idempotent across both states.

alter table schema_migrations add column if not exists checksum text;

comment on column schema_migrations.checksum is
    'SHA-256 of the migration body, recorded so a divergent applied migration trips ErrChecksumMismatch on next startup.';
