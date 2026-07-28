-- name: ClaimDedupeKey :one
-- Claims a dedupe key for an event (BR-13), returning the claiming event id.
--
-- A key that has never been seen inserts. A key whose window has passed is
-- reclaimed in place by the new event. A key still inside its window fails the
-- ON CONFLICT WHERE clause, updates nothing, and returns no rows — that empty
-- result is the duplicate signal.
--
-- One statement, so the check is atomic: two identical events racing each other
-- serialize on the primary key, and exactly one of them walks away with the
-- claim. Correctness therefore never depends on the cleanup job below.
INSERT INTO dedup_index (source_id, dedupe_key, event_id, expires_at)
VALUES (
    $1, $2, $3,
    now() + (sqlc.arg(window_seconds)::int * interval '1 second')
)
ON CONFLICT (source_id, dedupe_key) DO UPDATE
    SET event_id  = EXCLUDED.event_id,
        expires_at = EXCLUDED.expires_at
    WHERE dedup_index.expires_at <= now()
RETURNING event_id;

-- name: DeleteExpiredDedupEntries :execrows
-- Housekeeping for keys past their window, run periodically. ClaimDedupeKey
-- already reclaims expired rows in place, so this only keeps the table from
-- growing one row per unique key forever.
DELETE FROM dedup_index WHERE expires_at <= now();
