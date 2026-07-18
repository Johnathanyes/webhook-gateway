-- name: InsertDelivery :one
INSERT INTO deliveries (
    tenant_id,
    event_id,
    destination_id
) VALUES (
    $1, $2, $3
)
RETURNING id;

-- name: GetDelivery :one
SELECT * FROM deliveries
WHERE id = $1;

-- name: RecordDeliveryOutcome :one
UPDATE deliveries
SET status = $2,
    attempt_count = attempt_count + 1,
    last_attempted_at = now(),
    dead_lettered_at = CASE WHEN $2 = 'dead_lettered' THEN now() ELSE dead_lettered_at END,
    updated_at = now()
WHERE id = $1
RETURNING attempt_count;

-- name: ResetDeliveryForRecovery :exec
UPDATE deliveries
SET status = 'pending',
    dead_lettered_at = NULL,
    next_attempt_at = NULL,
    updated_at = now()
WHERE id = $1;

-- name: SetDeliveryRiverJobID :exec
-- Backfills the River job id onto a delivery once the job is enqueued, so the
-- queue row can be cross-referenced from the delivery while debugging (BR-17).
UPDATE deliveries
SET river_job_id = $2
WHERE id = $1;
