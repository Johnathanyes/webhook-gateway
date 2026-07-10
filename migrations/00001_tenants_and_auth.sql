-- +goose Up

-- Tenants exist even in OSS: exactly one row, inserted below. Cloud creates
-- more. Every other table hangs off tenant_id from day one so multi-tenancy
-- is a config difference, not a schema migration, when cloud launches.
CREATE TABLE tenants (
    id                     UUID PRIMARY KEY DEFAULT uuidv7(),
    name                   TEXT NOT NULL,
    slug                   TEXT NOT NULL UNIQUE,
    plan                   TEXT NOT NULL DEFAULT 'oss',   -- 'oss' | 'free' | 'starter' | 'team' | 'scale'
    events_quota_monthly   BIGINT,                        -- NULL = unlimited (OSS, and Scale w/ volume deal)
    retention_days         INT NOT NULL DEFAULT 3650,      -- OSS default = "effectively forever"; cloud tiers override
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The OSS seed row. Every self-hosted install has exactly this one tenant.
INSERT INTO tenants (id, name, slug, plan, events_quota_monthly, retention_days)
VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'default', 'oss', NULL, 3650)
ON CONFLICT DO NOTHING;

-- Single-user auth in OSS v1 (BR-27), OIDC-ready: password_hash and
-- oidc_subject are both nullable so either auth path works without a schema
-- change later. `role` exists now but is inert until RBAC (v2/BR-35) reads it.
CREATE TABLE users (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email          TEXT NOT NULL,
    password_hash  TEXT,                       -- NULL if OIDC-only
    oidc_subject   TEXT,                        -- provider "sub" claim
    role           TEXT NOT NULL DEFAULT 'admin', -- 'admin' | 'member' | 'viewer' (member/viewer unused pre-RBAC)
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, email)
);

-- API keys are how the CLI and any external caller authenticate (BR-24).
-- Only a hash is ever stored; key_prefix is what the UI shows so a user can
-- recognize a key without ever re-displaying the secret.
CREATE TABLE api_keys (
    id             UUID PRIMARY KEY DEFAULT uuidv7(),
    tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    key_prefix     TEXT NOT NULL,               -- e.g. "whg_live_ab12", shown in UI
    key_hash       TEXT NOT NULL,                -- sha256 of the full key
    scopes         TEXT[] NOT NULL DEFAULT '{}', -- e.g. {'events:read','sources:write'}
    created_by     UUID REFERENCES users(id) ON DELETE SET NULL,
    last_used_at   TIMESTAMPTZ,
    revoked_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (key_prefix)
);
CREATE INDEX idx_api_keys_tenant_active ON api_keys(tenant_id) WHERE revoked_at IS NULL;

-- Small singleton key/value store for instance-level state that isn't
-- tenant-scoped: telemetry instance id (BR-58), encryption key version in
-- use, schema/version bookkeeping. One row per key, no tenant_id.
CREATE TABLE instance_settings (
    key         TEXT PRIMARY KEY,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS instance_settings;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;