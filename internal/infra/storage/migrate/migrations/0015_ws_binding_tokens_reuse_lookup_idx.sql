-- 0015_ws_binding_tokens_reuse_lookup_idx.sql
--
-- OP #241 — back the FindUnexpiredBySubscriptionAndClient lookup with
-- an index that lets the hot path complete in one B-tree probe.
--
-- WHY a partial index on `consumed_at IS NULL` instead of `expires_at >
-- now()`: Postgres requires partial-index predicates to reference only
-- IMMUTABLE expressions. `now()` is STABLE, not IMMUTABLE, so a
-- predicate like `WHERE expires_at > now()` is rejected. The
-- `consumed_at IS NULL` predicate captures the same intent (rows that
-- have been bound are no longer reuse candidates) AND is index-eligible.
-- The handler-side query still filters `expires_at > $now` against the
-- caller-supplied clock; the partial predicate just shrinks the index
-- footprint to the unconsumed-tokens set.
--
-- This is NOT a unique index: two unconsumed unexpired rows for the
-- same (subscription_id, client_id) are legal under the OP #241 cold-
-- start path (cache miss + DB hit re-mints a fresh row to give the
-- caller a usable cleartext). Uniqueness would block that case.

create index if not exists ws_binding_tokens_reuse_lookup_idx
    on ws_binding_tokens (subscription_id, client_id, expires_at desc)
    where consumed_at is null;

comment on index ws_binding_tokens_reuse_lookup_idx is
    'OP #241: backs FindUnexpiredBySubscriptionAndClient — unconsumed rows by (subscription, client) ordered by expiry desc.';
