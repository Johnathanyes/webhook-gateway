-- name: InsertDestination :one
INSERT INTO destinations (
    tenant_id,
    name,
    url,
    auth_config,
    timeout_ms,
    rate_limit_per_second,
    max_attempts,
    backoff_base_seconds,
    backoff_max_seconds
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: GetDestination :one
SELECT * FROM destinations
WHERE id = $1 AND tenant_id = $2;

-- name: ListDestinations :many
SELECT * FROM destinations
WHERE tenant_id = $1
ORDER BY created_at DESC;

-- name: UpdateDestination :one
UPDATE destinations
SET
    name = $3,
    url = $4,
    auth_config = $5,
    timeout_ms = $6,
    rate_limit_per_second = $7,
    max_attempts = $8,
    backoff_base_seconds = $9,
    backoff_max_seconds = $10,
    updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: DeleteDestination :execrows
DELETE FROM destinations
WHERE id = $1 AND tenant_id = $2;

-- name: PauseDestination :one
UPDATE destinations
SET paused_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: ResumeDestination :one
UPDATE destinations
SET paused_at = NULL
WHERE id = $1 AND tenant_id = $2
RETURNING *;
