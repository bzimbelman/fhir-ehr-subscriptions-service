-- migrations/0016_partition_trigger_use_new_created_at.sql
--
-- Story #215 (OP #139): the resource_changes / ehr_events partition
-- triggers introduced in 0001_init.sql derive `created_month` from
-- `now()` rather than `NEW.created_at`. Backfill or replay writes
-- supplying a historical `created_at` therefore land in the
-- current-month partition, silently violating the schema invariant
--   created_month = date_trunc('month', created_at)
-- and breaking any read path that filters by month.
--
-- This migration replaces both BEFORE-INSERT trigger functions with
-- the corrected expression. CREATE OR REPLACE keeps the trigger
-- itself (set_resource_changes_created_month, set_ehr_events_created_month)
-- bound to the same function name so no DROP TRIGGER is required;
-- subsequent INSERTs see the new behavior immediately.
--
-- Forward-only. Existing rows are not rewritten — operators who
-- backfilled BEFORE this migration must run a one-shot UPDATE to
-- realign created_month with created_at (the sweeper docs cover the
-- procedure). New writes are correct from this migration onward.

create or replace function set_resource_changes_created_month()
returns trigger
language plpgsql
as $$
begin
    new.created_month := date_trunc('month', new.created_at)::date;
    return new;
end;
$$;

create or replace function set_ehr_events_created_month()
returns trigger
language plpgsql
as $$
begin
    new.created_month := date_trunc('month', new.created_at)::date;
    return new;
end;
$$;

insert into schema_migrations (version) values ('0016')
    on conflict (version) do nothing;
