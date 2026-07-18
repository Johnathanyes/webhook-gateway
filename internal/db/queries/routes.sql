-- name: InsertRoute :one
INSERT INTO routes (
    tenant_id,
    source_id,
    destination_id,
    enabled
) VALUES (
    $1, $2, $3, $4
)
RETURNING *;

-- name: GetRoute :one
SELECT * FROM routes
WHERE id = $1 AND tenant_id = $2;

-- name: ListRoutes :many
SELECT * FROM routes
WHERE tenant_id = $1
    AND (sqlc.narg('source_id')::uuid IS NULL OR source_id = sqlc.narg('source_id'))
    AND (sqlc.narg('destination_id')::uuid IS NULL OR destination_id = sqlc.narg('destination_id'))
ORDER BY created_at DESC;

-- name: ListEnabledDeliveryTargetsForSource :many
SELECT d.id AS destination_id,
       d.max_attempts,
       d.backoff_base_seconds,
       d.backoff_max_seconds
FROM routes r
JOIN destinations d ON d.id = r.destination_id
WHERE r.source_id = $1 AND r.enabled = true;

-- name: UpdateRouteEnabled :one
UPDATE routes
SET enabled = $3
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: DeleteRoute :execrows
DELETE FROM routes
WHERE id = $1 AND tenant_id = $2;
