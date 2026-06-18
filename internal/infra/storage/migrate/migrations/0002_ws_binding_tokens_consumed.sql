-- 0002_ws_binding_tokens_consumed.sql
--
-- Adds a `consumed_at` timestamp to ws_binding_tokens so a bind handler can
-- atomically claim a token with a single UPDATE ... WHERE consumed_at IS NULL
-- and fail closed on any retry. Prior to this migration the only single-use
-- enforcement was DELETE-after-Get, which races between two concurrent bind
-- attempts that read the same token before either deletes it.
--
-- Forward-compatible: column is added NULLable; existing rows are unconsumed.

alter table ws_binding_tokens
    add column if not exists consumed_at timestamptz;

-- Lookups during bind hit (token, consumed_at IS NULL) heavily; partial index.
create index if not exists ws_binding_tokens_unconsumed_idx
    on ws_binding_tokens (token)
    where consumed_at is null;
