-- name: GetInstanceSetting :one
SELECT value FROM instance_settings WHERE key = $1;

-- name: UpsertInstanceSetting :exec
INSERT INTO instance_settings (key, value)
VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now();
