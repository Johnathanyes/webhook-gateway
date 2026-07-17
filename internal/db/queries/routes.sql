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

-- name: ListEnabledDestinationIDsForSource :many
-- Destinations an event from this source fans out to: enabled routes only.
SELECT destination_id FROM routes
WHERE source_id = $1 AND enabled = true;

-- name: UpdateRouteEnabled :one
UPDATE routes
SET enabled = $3
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: DeleteRoute :execrows
DELETE FROM routes
WHERE id = $1 AND tenant_id = $2;
