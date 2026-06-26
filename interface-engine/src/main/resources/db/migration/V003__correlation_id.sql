-- V003 correlation_id column on ingested_messages (Epic #387, ticket #388).
--
-- Each row gets a server-assigned correlation id (UUID v4) when the
-- interface engine receives the inbound message. The same value is then
-- emitted on every log line for that row and on every outbound HTTP call
-- driven by it (matchbox $transform, HAPI POST), so one grep against
-- kubectl logs surfaces the full pipeline trace for a single message.
--
-- Nullable so rows that predate this migration are preserved as-is:
-- legacy rows continue through the worker without a correlation_id, and
-- the worker treats "no correlation_id" as "generate one on first
-- processing" (see IngestedMessageWorker.processOne — the row's stored
-- correlation_id is read into MDC if present, otherwise a fresh UUID is
-- minted before any processing log line is emitted).
--
-- Partial index on the column (excluding NULLs) is sized for the
-- "look up a row by correlation_id" admin operator query, which is the
-- main reason the column exists outside of pure logging. The index is
-- intentionally narrow — including NULL rows would index the entire
-- pre-#388 history without benefit.

ALTER TABLE ingested_messages
  ADD COLUMN correlation_id TEXT;

CREATE INDEX ix_ingested_correlation_id
    ON ingested_messages (correlation_id)
 WHERE correlation_id IS NOT NULL;
