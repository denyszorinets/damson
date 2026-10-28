-- name: SelectClaimableExecutions :many
SELECT id FROM executions
WHERE state = 'queued' AND scheduled_at <= sqlc.arg(now) AND pool = ANY(sqlc.arg(pools)::text[])
ORDER BY priority, scheduled_at, id
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;

-- name: SelectExpiredLeases :many
SELECT id, attempt_id FROM executions
WHERE state IN ('dispatched', 'running') AND lease_expires_at < sqlc.arg(now)
ORDER BY lease_expires_at, id
LIMIT sqlc.arg(row_limit)
FOR UPDATE SKIP LOCKED;

-- Monotonic: a lease is only ever extended. Every live attempt owned by the
-- controller is returned, whether or not its lease moved.
-- name: RenewLeases :many
UPDATE executions SET lease_expires_at = CASE
    WHEN lease_expires_at IS NULL OR lease_expires_at < sqlc.arg(lease_expires_at)::timestamptz
    THEN sqlc.arg(lease_expires_at)::timestamptz ELSE lease_expires_at END
WHERE attempt_id = ANY(sqlc.arg(attempt_ids)::text[]) AND controller_id = sqlc.arg(controller_id)
  AND state IN ('dispatched', 'running')
RETURNING attempt_id;

-- LockExecutionAttempt fences a transaction: it matches only while attempt_id
-- is current, and the share lock holds off a concurrent claim or requeue
-- (which update the row) until commit.
-- name: LockExecutionAttempt :one
SELECT id FROM executions WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) FOR SHARE;
