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
