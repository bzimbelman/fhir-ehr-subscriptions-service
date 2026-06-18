-- 0005_subscriptions_client_match_idx.sql
--
-- S-2.4: Push the FHIR `If-None-Exist` predicate into SQL. The handler
-- previously listed every subscription owned by a client and filtered
-- in Go (O(N) per POST). With the new
-- SubscriptionsStore.FindByClientAndCriteria query the database does
-- the matching directly — but only if a covering composite index
-- exists. Without one, Postgres falls back to a seq-scan per POST and
-- the predicate-push gain disappears at scale.
--
-- Index column order rationale:
--   1. client_id      — every If-None-Exist query is per-client;
--                       this is the most selective leading column.
--   2. channel_type   — small cardinality (rest-hook / websocket /
--                       email / message) but always supplied by
--                       LLD §4.1 search criteria.
--   3. topic_url      — usually supplied; high cardinality.
--   4. endpoint       — optional but common; trailing column lets
--                       postgres still use the index when omitted.
--
-- The status-tombstone filter (`status <> 'off'`) is intentionally NOT
-- in the index because the index covers the high-selectivity equality
-- columns; the small post-filter on status keeps the index narrow.
--
-- A non-CONCURRENTLY CREATE INDEX takes a brief ShareLock on the table
-- (writes block; reads do not). The subscriptions table is small in
-- every realistic deployment (one row per client × per channel × per
-- topic) so the lock window is sub-second; CONCURRENTLY would require
-- the migration runner to handle parallel-racer + CREATE INDEX
-- CONCURRENTLY interactions, which it currently does not.

create index if not exists subscriptions_client_match_idx
    on subscriptions (client_id, channel_type, topic_url, endpoint);

comment on index subscriptions_client_match_idx is
    'Covers SubscriptionsStore.FindByClientAndCriteria (S-2.4 — If-None-Exist predicate pushdown).';
