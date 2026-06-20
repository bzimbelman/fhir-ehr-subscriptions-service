-- 0009_dead_letters_key_version.sql
--
-- OP #138: dead_letters carries an encrypted payload_redacted bytea but
-- no key_version column. The codec's active version at insert time was
-- baked only into the AAD; once the active key rotates, every existing
-- dead-letter payload becomes unrecoverable because the row no longer
-- knows which key wrapped it.
--
-- The fix is the same shape as 0003 (pending_pairs) and the columns
-- already present on hl7_message_queue, resource_changes, ehr_events,
-- and adapter_state: a key_version int NOT NULL DEFAULT 1 captured per
-- row at insert time.
--
-- Existing rows take the default value 1 (the historical-and-only key
-- this service has ever shipped with). That assumption holds because
-- this service is greenfield; production deployments that actually have
-- rotated keys must split this into expand/contract.

alter table dead_letters
    add column if not exists key_version int not null default 1;

comment on column dead_letters.key_version is
    'Codec key version that wrapped payload_redacted; required so a row written under v1 still decrypts after the active key rotates.';
