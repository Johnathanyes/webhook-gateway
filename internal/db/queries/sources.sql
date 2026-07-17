-- name: GetSourceByEndpointPath :one
-- Ingest looks a source up by its path segment to decide which
-- verifier runs and which tenant/source the event belongs to.
SELECT * FROM sources
WHERE endpoint_path = $1;

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
    verification_config
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;
