-- SQLite has no row locks; writes are serialized by the immediate transaction.
-- name: SelectClaimableExecutions :many
SELECT id FROM executions
WHERE state = 'queued' AND scheduled_at <= sqlc.arg(now) AND pool = sqlc.arg(pool)
ORDER BY priority, scheduled_at, id
LIMIT sqlc.arg(row_limit);

-- name: SelectExpiredLeases :many
SELECT id, attempt_id FROM executions
WHERE state IN ('dispatched', 'running') AND lease_expires_at < sqlc.arg(now)
ORDER BY lease_expires_at, id
LIMIT sqlc.arg(row_limit);
