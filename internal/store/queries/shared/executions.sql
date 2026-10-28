-- name: InsertExecution :execrows
INSERT INTO executions (id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at,
                        priority, pool, created_at)
VALUES (sqlc.arg(id), sqlc.arg(run_id), sqlc.arg(step_instance_id), sqlc.arg(attempt_no),
        sqlc.arg(attempt_id), 'queued', sqlc.arg(scheduled_at), sqlc.arg(priority), sqlc.arg(pool),
        sqlc.arg(created_at));

-- name: LatestAttemptNo :one
SELECT attempt_no FROM executions
WHERE step_instance_id = sqlc.arg(step_instance_id) ORDER BY attempt_no DESC LIMIT 1;

-- name: GetExecution :one
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at
FROM executions WHERE id = sqlc.arg(id);

-- name: GetExecutionByAttempt :one
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at
FROM executions WHERE attempt_id = sqlc.arg(attempt_id);

-- name: ListExecutionsByRun :many
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at
FROM executions WHERE run_id = sqlc.arg(run_id) ORDER BY step_instance_id, attempt_no;

-- name: ClaimExecutionCAS :execrows
UPDATE executions SET state = 'dispatched', attempt_id = sqlc.arg(attempt_id), agent_id = sqlc.arg(agent_id),
       controller_id = sqlc.arg(controller_id), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND state = 'queued';

-- name: MarkExecutionRunningCAS :execrows
UPDATE executions SET state = 'running', started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state = 'dispatched';

-- name: RenewLeaseCAS :execrows
UPDATE executions SET lease_expires_at = sqlc.arg(lease_expires_at)
WHERE attempt_id = sqlc.arg(attempt_id) AND controller_id = sqlc.arg(controller_id)
  AND state IN ('dispatched', 'running');

-- name: MarkExecutionLostCAS :execrows
UPDATE executions SET state = 'lost', finished_at = sqlc.arg(finished_at), error = 'lease expired'
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state IN ('dispatched', 'running')
  AND lease_expires_at < sqlc.arg(now);

-- name: CompleteExecutionCAS :execrows
UPDATE executions SET state = sqlc.arg(state), outputs = sqlc.arg(outputs), error = sqlc.arg(error),
       finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state IN ('dispatched', 'running');

-- name: RequeueExecutionCAS :execrows
UPDATE executions SET state = 'queued', scheduled_at = sqlc.arg(scheduled_at), agent_id = NULL,
       controller_id = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state = 'dispatched';

-- LockExecutionAttempt is a no-op write that fences the transaction: it
-- matches only while attempt_id is current, and holds the row against a
-- concurrent claim or requeue until commit.
-- name: LockExecutionAttempt :execrows
UPDATE executions SET attempt_id = attempt_id
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id);
