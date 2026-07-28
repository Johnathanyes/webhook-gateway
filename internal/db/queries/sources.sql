-- name: GetSourceByEndpointPath :one
-- Ingest looks a source up by its path segment to decide which
-- verifier runs and which tenant/source the event belongs to.
SELECT * FROM sources
WHERE endpoint_path = $1;

-- name: GetSource :one
-- The test-event generator and other admin lookups fetch a source by
-- id, scoped to the tenant like every other admin-authed query.
SELECT * FROM sources
WHERE id = $1 AND tenant_id = $2;

-- name: GetSourceByName :one
SELECT * FROM sources
WHERE tenant_id = $1 AND name = $2
ORDER BY created_at ASC
LIMIT 1;

-- name: ListSources :many
SELECT * FROM sources
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: InsertSource :one
INSERT INTO sources (
    tenant_id,
    name,
    provider_type,
    endpoint_path,
    signing_secret_encrypted,
    signing_secret_key_version,
    verification_config,
    dedupe_enabled,
    dedupe_strategy,
    dedupe_field_path,
    dedupe_window_seconds
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
)
RETURNING *;
