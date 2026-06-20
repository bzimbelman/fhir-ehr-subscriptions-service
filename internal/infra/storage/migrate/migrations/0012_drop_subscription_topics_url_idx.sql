-- 0012_drop_subscription_topics_url_idx.sql
--
-- OP #143: subscription_topics_url_idx on (url) is redundant — the
-- table's UNIQUE (url, version) constraint already produces an
-- implicit btree on (url, version), and Postgres uses the leading
-- column of a multi-column index for equality and prefix lookups on
-- url. Carrying both costs a write on every Insert and gives no read
-- benefit.

drop index if exists subscription_topics_url_idx;
