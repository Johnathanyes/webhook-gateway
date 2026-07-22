-- +goose Up

-- Per-destination, per-condition alert cooldown state
CREATE TABLE alert_state (
    destination_id  UUID NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
    condition       TEXT NOT NULL, -- 'dlq' | 'failure_rate'
    last_fired_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (destination_id, condition)
);

-- +goose Down
DROP TABLE IF EXISTS alert_state;
