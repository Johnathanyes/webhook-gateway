-- +goose Up

-- A source = one inbound endpoint. provider_type is a slug into the
-- source-definition catalog (internal/sourcedef/catalog/*.yaml) that drives
-- which verifier runs (BR-02, BR-03, BR-37). endpoint_path is the unique,
-- unguessable path segment the provider posts to (BR-01).
CREATE TABLE sources (
    id                          UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id                   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                        TEXT NOT NULL,
    provider_type               TEXT NOT NULL,            -- 'stripe' | 'github' | 'generic_hmac' | 'none' | ...
    endpoint_path               TEXT NOT NULL,             -- e.g. "src_9f2a7c1e8b4d..."
    signing_secret_encrypted    BYTEA,                      -- envelope-encrypted (nonce+ciphertext); NULL if provider_type='none'
    signing_secret_key_version  INT,                        -- which master key version encrypted the secret (BR-26, key rotation)
    verification_config         JSONB NOT NULL DEFAULT '{}', -- provider-specific extras: header names, timestamp tolerance, etc.
    dedupe_enabled              BOOLEAN NOT NULL DEFAULT false,
    dedupe_strategy             TEXT,                        -- 'exact' | 'field', required if dedupe_enabled
    dedupe_field_path           TEXT,                         -- e.g. "$.id", required if dedupe_strategy='field'
    dedupe_window_seconds       INT NOT NULL DEFAULT 300,
    paused_at                   TIMESTAMPTZ,                  -- ingest itself is never paused; this pauses processing/delivery of new events
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (endpoint_path),
    CONSTRAINT chk_dedupe_strategy CHECK (
        NOT dedupe_enabled OR dedupe_strategy IN ('exact', 'field')
    ),
    CONSTRAINT chk_dedupe_field_path CHECK (
        dedupe_strategy IS DISTINCT FROM 'field' OR dedupe_field_path IS NOT NULL
    )
);
CREATE INDEX idx_sources_tenant ON sources(tenant_id);

-- A destination = one outbound target. Retry/backoff policy lives here
-- per-destination (BR-08, BR-11) rather than globally, since different
-- consumers tolerate different pacing.
CREATE TABLE destinations (
    id                     UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    url                    TEXT NOT NULL,
    auth_config            JSONB NOT NULL DEFAULT '{}',  -- e.g. {"type":"bearer","token_encrypted":"..."} or custom headers
    timeout_ms             INT NOT NULL DEFAULT 10000,     -- BR-11: slow endpoints are treated as failures past this
    rate_limit_per_second  INT,                             -- NULL = unlimited; BR-10 backpressure/pacing
    max_attempts           INT NOT NULL DEFAULT 8,          -- BR-08 default: ~8 attempts over ~3 days
    backoff_base_seconds   INT NOT NULL DEFAULT 30,
    backoff_max_seconds    INT NOT NULL DEFAULT 43200,       -- 12h ceiling between attempts
    paused_at              TIMESTAMPTZ,                       -- BR-12: pause/resume per destination
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_destinations_tenant ON destinations(tenant_id);

-- Routes are the many-to-many binding between sources and destinations —
-- this table alone is what gives you fan-out (1 source -> N destinations)
-- and fan-in (N sources -> 1 destination) for free (BR-15).
CREATE TABLE routes (
    id              UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source_id       UUID NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
    destination_id  UUID NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_id, destination_id)
);
CREATE INDEX idx_routes_source_enabled ON routes(source_id) WHERE enabled;
CREATE INDEX idx_routes_destination ON routes(destination_id);

-- +goose Down
DROP TABLE IF EXISTS routes;
DROP TABLE IF EXISTS destinations;
DROP TABLE IF EXISTS sources;