-- name: InsertReplay :one
-- Opens a bulk-replay record
INSERT INTO replays (
    tenant_id,
    requested_by,
    filter
) VALUES (
    $1, $2, $3
)
RETURNING id, tenant_id, filter, status, matched_count, requeued_count, created_at, completed_at;

-- name: GetReplay :one
SELECT id, tenant_id, requested_by, filter, status, matched_count, requeued_count, created_at, completed_at
FROM replays
WHERE id = $1;

-- name: FinishReplay :exec
-- Records the terminal outcome of a bulk replay: 'completed' with the matched
-- and requeued counts, or 'failed'.
UPDATE replays
SET status = $2,
    matched_count = $3,
    requeued_count = $4,
    completed_at = now()
WHERE id = $1;
