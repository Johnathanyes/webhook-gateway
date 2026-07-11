-- +goose Up

-- Tracks bulk replay operations (BR-18: "bulk replay by filter"). Single-
-- event replay doesn't need a row here — it just creates a new delivery +
-- River job directly. This table exists so a bulk replay (which can touch
-- thousands of deliveries) is itself observable and auditable: who ran it,
-- what filter, how many matched, how many actually got requeued.
CREATE TABLE replays (
    id                UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    requested_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    filter            JSONB NOT NULL,   -- serialized filter: time range, status, source_id, destination_id
    status            TEXT NOT NULL DEFAULT 'running', -- 'running' | 'completed' | 'failed'
    matched_count     INT,
    requeued_count    INT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at      TIMESTAMPTZ,
    CONSTRAINT chk_replay_status CHECK (status IN ('running', 'completed', 'failed'))
);
CREATE INDEX idx_replays_tenant_created ON replays(tenant_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS replays;