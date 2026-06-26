-- V002 ingested_messages table (Epic #378, ticket #380).
--
-- Durable inbound store for every message the interface engine receives,
-- across all source protocols. v1 only feeds HL7v2-over-MLLP in (via
-- IngestRoutes); the schema is intentionally multi-protocol-ready so
-- FHIR REST, EHR-native APIs, etc. can land here as new sources without
-- requiring another migration / table.
--
-- Lifecycle (status column):
--   RECEIVED      -- row written by the inbound route, raw payload + metadata only
--   TRANSFORMING  -- async worker (ticket #382) has claimed it; transform in flight
--   DELIVERED     -- transform + HAPI POST both succeeded; delivered_at set
--   FAILED        -- non-retryable failure (bad message, schema violation, etc.)
--   DEAD_LETTER   -- retry budget exhausted (attempt_count >= max); operator action
--
-- Idempotency: UNIQUE (source_system, source_id) lets receivers safely retry
-- without double-writing. The route does an INSERT ... ON CONFLICT DO NOTHING
-- (or upsert-style lookup via the JPA repo) so a re-delivered HL7 ACK is a no-op.
--
-- next_attempt_at supports the retry scheduler in ticket #382 (partial index
-- below). Worker polls "status='RECEIVED' OR (status in failure-retryable and
-- next_attempt_at <= now())" in received_at order.

CREATE TYPE ingested_message_status AS ENUM (
    'RECEIVED', 'TRANSFORMING', 'DELIVERED', 'FAILED', 'DEAD_LETTER'
);

CREATE TYPE ingested_message_source_protocol AS ENUM (
    'HL7V2_MLLP',
    'FHIR_REST',
    'EHR_NATIVE_API',
    'OTHER'
);

CREATE TABLE ingested_messages (
    id              BIGSERIAL PRIMARY KEY,
    received_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    source_protocol ingested_message_source_protocol NOT NULL,
    source_system   TEXT NOT NULL,
    source_id       TEXT NOT NULL,
    message_type    TEXT NOT NULL,
    raw_message     TEXT NOT NULL,
    raw_content_type TEXT NOT NULL,
    status          ingested_message_status NOT NULL DEFAULT 'RECEIVED',
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    last_attempt_at TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ,
    last_error      TEXT,
    delivered_at    TIMESTAMPTZ,
    UNIQUE (source_system, source_id)
);

CREATE INDEX ix_ingested_status_received ON ingested_messages (status, received_at);
CREATE INDEX ix_ingested_next_attempt    ON ingested_messages (next_attempt_at) WHERE next_attempt_at IS NOT NULL;
CREATE INDEX ix_ingested_received_at     ON ingested_messages (received_at);
