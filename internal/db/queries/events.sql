-- name: GetEventForDelivery :one
SELECT raw_body, content_type FROM events
WHERE id = $1;

-- name: ListEvents :many
SELECT id, tenant_id, source_id, content_type, dedupe_key, verified, received_at
FROM events e
WHERE e.tenant_id = $1
  AND (sqlc.narg('source_id')::uuid IS NULL OR e.source_id = sqlc.narg('source_id'))
  AND (sqlc.narg('verified')::boolean IS NULL OR e.verified = sqlc.narg('verified'))
  AND (sqlc.narg('after')::timestamptz IS NULL OR e.received_at >= sqlc.narg('after'))
  AND (sqlc.narg('before')::timestamptz IS NULL OR e.received_at <= sqlc.narg('before'))
  AND (sqlc.narg('search')::text IS NULL OR e.search_vector @@ websearch_to_tsquery('english', sqlc.narg('search')))
  AND (sqlc.narg('delivery_status')::text IS NULL OR EXISTS (
      SELECT 1 FROM deliveries d WHERE d.event_id = e.id AND d.status = sqlc.narg('delivery_status')))
  AND (sqlc.narg('cursor')::uuid IS NULL OR e.id < sqlc.narg('cursor'))
ORDER BY e.id DESC
LIMIT sqlc.arg('page_limit');

-- name: GetEvent :one
-- Full event including raw headers and raw body, for the detail view and the
-- CLI's replay-to-localhost.
SELECT id, tenant_id, source_id, raw_headers, raw_body, content_type,
       parsed_body, dedupe_key, verified, received_at
FROM events
WHERE id = $1 AND tenant_id = $2;

-- name: ListEventDeliveries :many
-- Every delivery spawned from an event, for the trace view.
SELECT id, tenant_id, event_id, destination_id, river_job_id, status,
       attempt_count, next_attempt_at, last_attempted_at, dead_lettered_at,
       created_at, updated_at
FROM deliveries
WHERE event_id = $1 AND tenant_id = $2
ORDER BY created_at;

-- name: ListEventDeliveryAttempts :many
-- Every HTTP attempt across all of an event's deliveries, flattened and
-- ordered so the handler can group by delivery_id for the timeline.
SELECT da.id, da.delivery_id, da.attempt_number, da.request_headers,
       da.response_status_code, da.response_headers, da.response_body_truncated,
       da.error, da.duration_ms, da.attempted_at
FROM delivery_attempts da
JOIN deliveries d ON d.id = da.delivery_id
WHERE d.event_id = $1 AND d.tenant_id = $2
ORDER BY da.delivery_id, da.attempt_number;

-- name: InsertEvent :one
-- Persists an inbound webhook: raw_headers as JSONB,
-- raw_body as the exact bytes received, parsed_body a best-effort JSON parse
-- (NULL when unstructured), and verified the signature-check result.
INSERT INTO events (
    tenant_id,
    source_id,
    raw_headers,
    raw_body,
    content_type,
    parsed_body,
    dedupe_key,
    verified
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, received_at;
