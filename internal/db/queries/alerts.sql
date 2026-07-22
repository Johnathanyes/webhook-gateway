-- name: DeadLetteredSince :many
-- Destinations that had at least one delivery dead-letter within the window,
-- for the DLQ alert condition (BR-19).
SELECT DISTINCT d.destination_id, dest.name
FROM deliveries d
JOIN destinations dest ON dest.id = d.destination_id
WHERE d.status = 'dead_lettered' AND d.dead_lettered_at >= $1;

-- name: DeliveryFailureRatesSince :many
-- Per-destination terminal delivery counts within the window, for the
-- failure-rate condition. Only terminal outcomes count toward the rate; a
-- delivery still retrying ('failed'/'pending') is not yet a success or a loss.
SELECT d.destination_id, dest.name,
       count(*) AS total,
       count(*) FILTER (WHERE d.status = 'dead_lettered') AS failures
FROM deliveries d
JOIN destinations dest ON dest.id = d.destination_id
WHERE d.status IN ('succeeded', 'dead_lettered') AND d.updated_at >= $1
GROUP BY d.destination_id, dest.name;

-- name: GetAlertLastFired :one
SELECT last_fired_at FROM alert_state
WHERE destination_id = $1 AND condition = $2;

-- name: MarkAlertFired :exec
INSERT INTO alert_state (destination_id, condition, last_fired_at)
VALUES ($1, $2, $3)
ON CONFLICT (destination_id, condition) DO UPDATE SET last_fired_at = $3;
