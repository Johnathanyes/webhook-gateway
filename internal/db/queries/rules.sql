-- name: ListEnabledRulesForSource :many
-- Evaluation order: lower priority first, created_at as the tiebreaker so
-- two rules at the same priority evaluate in creation order, deterministically.
SELECT * FROM rules
WHERE source_id = $1 AND enabled = true
ORDER BY priority, created_at;

-- name: InsertRule :one
INSERT INTO rules (tenant_id, source_id, name, expression, action, route_destination_ids, priority, enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListRules :many
SELECT * FROM rules
WHERE tenant_id = $1
ORDER BY source_id, priority, created_at;

-- name: GetRule :one
SELECT * FROM rules
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateRule :one
UPDATE rules
SET name = $3, expression = $4, action = $5, route_destination_ids = $6,
    priority = $7, enabled = $8, updated_at = now()
WHERE id = $1 AND tenant_id = $2
RETURNING *;

-- name: DeleteRule :execrows
DELETE FROM rules
WHERE id = $1 AND tenant_id = $2;
