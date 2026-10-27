-- name: UpsertControllerInstance :exec
INSERT INTO controller_instances (id, internal_url, started_at, last_seen)
VALUES (sqlc.arg(id), sqlc.arg(internal_url), sqlc.arg(started_at), sqlc.arg(last_seen))
ON CONFLICT (id) DO UPDATE SET
    internal_url = excluded.internal_url,
    started_at = excluded.started_at,
    last_seen = excluded.last_seen;

-- name: HeartbeatControllerInstance :execrows
UPDATE controller_instances SET last_seen = sqlc.arg(last_seen)
WHERE id = sqlc.arg(id);

-- name: DeleteControllerInstance :execrows
DELETE FROM controller_instances WHERE id = sqlc.arg(id);

-- name: ListControllerInstances :many
SELECT id, internal_url, started_at, last_seen
FROM controller_instances
ORDER BY id;
