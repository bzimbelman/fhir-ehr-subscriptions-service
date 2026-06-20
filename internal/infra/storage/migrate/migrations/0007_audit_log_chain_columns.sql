-- 0007_audit_log_chain_columns.sql
--
-- Story #106: align audit_log columns with what
-- internal/infra/observability/audit/pgstore.go reads/writes.
-- Migration 0001 creates `hash`, `prev_hash`, `canonical_form` and no
-- `payload`; the application code SELECTs/INSERTs `chain_hash`,
-- `prior_hash`, `chain_input`, `payload`. `fhir-subs audit verify`
-- fails immediately on any DB initialized only from 0001 because the
-- column names don't exist.
--
-- Story #107: drop the server-side `default now()` on `occurred_at`.
-- The application hashes the application-supplied OccurredAt into
-- chain_input; if the DB substitutes its own clock value, the on-disk
-- timestamp diverges from the bytes the chain_hash was computed over
-- and external verifiers cannot validate the chain.
--
-- 0001 left actor_id / target_kind / target_id / correlation_id
-- nullable; pgstore.IterateRows scans those columns into Go strings
-- (and a uuid.UUID), which fails on NULL. Tighten to NOT NULL with a
-- safe backfill so already-migrated test environments don't fail the
-- ALTER.
--
-- OP #214: every body must survive a partial-apply retry. Postgres
-- has no `RENAME COLUMN IF EXISTS`, so we guard each rename with a
-- DO block that checks information_schema. ADD COLUMN gets
-- `IF NOT EXISTS`. ALTER COLUMN ... DROP DEFAULT and SET NOT NULL
-- are intrinsically idempotent (re-applying is a no-op once the
-- target state is reached).

do $$
begin
    if exists (select 1 from information_schema.columns
               where table_name = 'audit_log' and column_name = 'hash') then
        alter table audit_log rename column hash to chain_hash;
    end if;
end;
$$;

do $$
begin
    if exists (select 1 from information_schema.columns
               where table_name = 'audit_log' and column_name = 'prev_hash') then
        alter table audit_log rename column prev_hash to prior_hash;
    end if;
end;
$$;

do $$
begin
    if exists (select 1 from information_schema.columns
               where table_name = 'audit_log' and column_name = 'canonical_form') then
        alter table audit_log rename column canonical_form to chain_input;
    end if;
end;
$$;

alter table audit_log add column if not exists payload jsonb;

alter table audit_log alter column occurred_at drop default;

update audit_log set actor_id = '' where actor_id is null;
update audit_log set target_kind = '' where target_kind is null;
update audit_log set target_id = '' where target_id is null;
update audit_log set correlation_id = '00000000-0000-0000-0000-000000000000'::uuid where correlation_id is null;

alter table audit_log alter column actor_id set not null;
alter table audit_log alter column target_kind set not null;
alter table audit_log alter column target_id set not null;
alter table audit_log alter column correlation_id set not null;
alter table audit_log alter column prior_hash set not null;
