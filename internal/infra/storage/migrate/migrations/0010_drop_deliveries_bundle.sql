-- 0010_drop_deliveries_bundle.sql
--
-- OP #139: deliveries.bundle bytea was declared in 0001_init.sql but is
-- never written, read, or selected by any production code path. The
-- "bundle persisted at delivery time" claim implicit in the schema is
-- unrealized — channels render their notification bundle on the fly
-- from the underlying ehr_event row and ship it directly to the
-- subscriber without a database round-trip.
--
-- Drop the column rather than wire up persistence: keeping a column
-- that nobody writes invites future readers to assume it carries data
-- and design retention/replay paths around the wrong source of truth.
-- If a future story actually needs persisted bundles, it can re-add the
-- column under expand/contract together with a write path.

alter table deliveries drop column if exists bundle;
