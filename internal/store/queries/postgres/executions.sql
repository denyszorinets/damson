-- name: SelectClaimableExecutions :many
SELECT id FROM executions
WHERE state = 'queued' AND scheduled_at <= sqlc.arg(now) AND pool = sqlc.arg(pool)
ORDER BY priority, scheduled_at, id
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;

-- name: SelectExpiredLeases :many
SELECT id, attempt_id FROM executions
WHERE state IN ('dispatched', 'running') AND lease_expires_at < sqlc.arg(now)
ORDER BY lease_expires_at, id
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;
