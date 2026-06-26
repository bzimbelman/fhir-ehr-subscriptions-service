-- V001 baseline (Epic #378, ticket #379)
--
-- Marker migration so Flyway records a baseline version in
-- flyway_schema_history. Real schema starts in V002 (ticket #380 —
-- ingested_messages table and its supporting types / indexes).
--
-- An empty body is valid SQL for Postgres; Flyway will INSERT a row into
-- flyway_schema_history pointing at this file. Subsequent migrations
-- chain off it.

SELECT 1;
