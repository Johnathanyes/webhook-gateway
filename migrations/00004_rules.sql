-- +goose Up

-- Declarative filter/route rules (BR-14), evaluated in priority order
-- against an event (headers, parsed_body, source metadata) via CEL-Go
-- before fan-out happens. 'drop' stops the event from reaching any
-- destination; 'route' overrides the default fan-out set (all enabled
-- routes for the source) with an explicit destination list — useful for
-- "only send high-value orders to the Slack alert destination" type rules.
CREATE TABLE rules (
    id                      UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id               UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_id               UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    name                    TEXT NOT NULL,
    expression              TEXT NOT NULL,   -- CEL expression, e.g. `body.amount > 10000`
    action                  TEXT NOT NULL,    -- 'drop' | 'route'
    route_destination_ids   UUID[],           -- required when action='route'; ignored when action='drop'
    priority                INT NOT NULL DEFAULT 100, -- lower runs first; first matching rule wins
    enabled                 BOOLEAN NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_rule_action CHECK (action IN ('drop', 'route')),
    CONSTRAINT chk_rule_route_destinations CHECK (
        action <> 'route' OR (route_destination_ids IS NOT NULL AND array_length(route_destination_ids, 1) > 0)
    )
);
CREATE INDEX idx_rules_source_priority ON rules(source_id, priority) WHERE enabled;

-- +goose Down
DROP TABLE IF EXISTS rules;