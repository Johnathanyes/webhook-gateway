-- name: InsertDelivery :one
-- Creates the pending delivery for an (event, destination) pair produced by
-- fan-out at ingest. status defaults to 'pending' and attempt_count to 0; the
-- River job that drives it is enqueued in the same transaction and its id
-- backfilled via SetDeliveryRiverJobID.
INSERT INTO deliveries (
    tenant_id,
    event_id,
    destination_id
) VALUES (
    $1, $2, $3
)
RETURNING id;

-- name: SetDeliveryRiverJobID :exec
-- Backfills the River job id onto a delivery once the job is enqueued, so the
-- queue row can be cross-referenced from the delivery while debugging (BR-17).
UPDATE deliveries
SET river_job_id = $2
WHERE id = $1;
