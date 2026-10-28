-- SQLite has no row locks; writes are serialized by the immediate transaction.
-- name: SelectClaimableExecutions :many
SELECT id FROM executions
WHERE state = 'queued' AND scheduled_at <= ? AND pool IN (sqlc.slice(pools))
ORDER BY priority, scheduled_at, id
LIMIT ?;

-- name: SelectExpiredLeases :many
SELECT id, attempt_id FROM executions
WHERE state IN ('dispatched', 'running') AND lease_expires_at < sqlc.arg(now)
ORDER BY lease_expires_at, id
LIMIT sqlc.arg(row_limit);

-- Monotonic: a lease is only ever extended (a dispatched or running row always
-- has a lease, so max() never sees NULL). Every live attempt owned by the
-- controller is returned, whether or not its lease moved. Plain ? parameters:
-- sqlc.slice and numbered parameters do not combine.
-- name: RenewLeases :many
UPDATE executions SET lease_expires_at = max(lease_expires_at, ?)
WHERE attempt_id IN (sqlc.slice(attempt_ids)) AND controller_id = ?
  AND state IN ('dispatched', 'running')
RETURNING attempt_id;

-- LockExecutionAttempt is a no-op write that fences the transaction: it
-- matches only while attempt_id is current. SQLite serializes writers.
-- name: LockExecutionAttempt :execrows
UPDATE executions SET attempt_id = attempt_id
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id);
