-- +goose Up

-- Set when a rule with action='drop' matched the event at ingest:
-- the event is stored — auditable and still replayable — but produced no
-- deliveries. Holds the name of the rule that dropped it; NULL = not dropped.
ALTER TABLE events ADD COLUMN dropped_reason TEXT;

-- +goose Down
ALTER TABLE events DROP COLUMN IF EXISTS dropped_reason;
