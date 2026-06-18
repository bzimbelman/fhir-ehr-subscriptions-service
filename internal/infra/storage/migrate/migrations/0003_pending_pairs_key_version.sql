-- 0003_pending_pairs_key_version.sql
--
-- Adds key_version to pending_pairs so the codec key used to encrypt
-- pending_resource is recorded with the row instead of being assumed
-- to be 1. This is the missing piece of the column-level encryption
-- contract in docs/high-level-design/decisions/0008 (audit B-21, B-22):
-- every PHI-bearing column has a sibling key_version int4, except
-- pending_pairs which the original migration omitted.
--
-- Forward-compatible: existing rows are stamped key_version=1, which
-- is the only version any deployed instance has used to date.

alter table pending_pairs
    add column if not exists key_version int not null default 1;

comment on column pending_pairs.key_version is 'Codec key version used to encrypt pending_resource; required for key rotation.';
