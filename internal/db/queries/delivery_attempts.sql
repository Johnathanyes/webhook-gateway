-- name: InsertDeliveryAttempt :exec
-- Records one HTTP try against a destination for the trace view.
INSERT INTO delivery_attempts (
    delivery_id,
    attempt_number,
    request_headers,
    response_status_code,
    response_headers,
    response_body_truncated,
    error,
    duration_ms
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
);
