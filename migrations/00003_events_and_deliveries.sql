-- +goose Up

-- The raw thing that arrived. raw_body is verbatim bytes (BR-04: JSON,
-- form-encoded, or raw/XML) so signature re-verification and replay are
-- always byte-exact. parsed_body is a best-effort JSON parse used for
-- filtering (BR-14), dedupe field extraction, and search — NULL when the
-- body isn't structured (e.g. raw XML with no parser configured yet).
CREATE TABLE events (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_id      UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    raw_headers    JSONB NOT NULL,
    raw_body       BYTEA NOT NULL,
    content_type   TEXT,
    parsed_body    JSONB,
    dedupe_key     TEXT,             -- computed at ingest time per source.dedupe_strategy; NULL if dedupe disabled
    verified       BOOLEAN NOT NULL,  -- result of signature verification (BR-02/03)
    received_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_events_tenant_received ON events(tenant_id, received_at DESC);
CREATE INDEX idx_events_source_received ON events(source_id, received_at DESC);
CREATE INDEX idx_events_parsed_body_gin ON events USING GIN (parsed_body jsonb_path_ops);
CREATE INDEX idx_events_headers_gin ON events USING GIN (raw_headers jsonb_path_ops);
-- Full-text search across headers+body for the event log search (BR-16).
-- Generated column keeps the tsvector in sync automatically; kept separate
-- from the GIN jsonb indexes above because free-text search and structured
-- field search are different query shapes.
ALTER TABLE events ADD COLUMN search_vector tsvector
    GENERATED ALWAYS AS (
        to_tsvector('english', coalesce(parsed_body::text, '') || ' ' || coalesce(raw_headers::text, ''))
    ) STORED;
CREATE INDEX idx_events_search_vector ON events USING GIN (search_vector);

-- Dedup is a separate small lookup table rather than a query against
-- `events` directly, so a dedupe check at ingest time is a fast indexed
-- point-lookup instead of a time-range scan over a table that grows
-- unbounded. Rows are cleaned up by a background job once expires_at
-- passes; the window (BR-13) is enforced by that expiry, not by a query
-- filter.
CREATE TABLE dedup_index (
    source_id    UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    dedupe_key   TEXT NOT NULL,
    event_id     UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    expires_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (source_id, dedupe_key)
);
CREATE INDEX idx_dedup_index_expires ON dedup_index(expires_at);

-- One row per (event, destination) pair — the unit of delivery state that
-- fan-out produces. This is the parent of the attempt history below, and
-- it's what BR-09 (dead-letter), BR-12 (pause/resume), and the trace view
-- (BR-17) all key off. river_job_id cross-references the River job driving
-- this delivery's scheduling, for when you need to go look at the queue
-- directly while debugging.
CREATE TABLE deliveries (
    id                 UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    event_id           UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    destination_id     UUID NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
    river_job_id       BIGINT,
    status             TEXT NOT NULL DEFAULT 'pending', -- 'pending' | 'succeeded' | 'failed' | 'dead_lettered' | 'paused'
    attempt_count      INT NOT NULL DEFAULT 0,
    next_attempt_at    TIMESTAMPTZ,
    last_attempted_at  TIMESTAMPTZ,
    dead_lettered_at   TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_delivery_status CHECK (
        status IN ('pending', 'succeeded', 'failed', 'dead_lettered', 'paused')
    )
);
CREATE INDEX idx_deliveries_tenant_status ON deliveries(tenant_id, status);
CREATE INDEX idx_deliveries_event ON deliveries(event_id);
CREATE INDEX idx_deliveries_destination_status ON deliveries(destination_id, status);
CREATE INDEX idx_deliveries_next_attempt ON deliveries(next_attempt_at) WHERE status = 'pending';

-- Every individual HTTP try against a destination, kept for the trace view
-- (BR-17: request/response bodies per attempt) and for debugging why a
-- delivery failed. response_body_truncated is capped in application code
-- before insert so one chatty destination can't bloat this table.
CREATE TABLE delivery_attempts (
    id                       UUID PRIMARY KEY DEFAULT uuidv7(),
    delivery_id              UUID NOT NULL REFERENCES deliveries(id) ON DELETE CASCADE,
    attempt_number           INT NOT NULL,
    request_headers          JSONB,
    response_status_code     INT,
    response_headers         JSONB,
    response_body_truncated  TEXT,
    error                     TEXT,   -- network/timeout error when there's no HTTP response at all
    duration_ms               INT,
    attempted_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (delivery_id, attempt_number)
);
CREATE INDEX idx_delivery_attempts_delivery ON delivery_attempts(delivery_id, attempt_number);

-- +goose Down
DROP TABLE IF EXISTS delivery_attempts;
DROP TABLE IF EXISTS deliveries;
DROP TABLE IF EXISTS dedup_index;
DROP TABLE IF EXISTS events;