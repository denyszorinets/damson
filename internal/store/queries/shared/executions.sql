-- name: InsertExecution :execrows
INSERT INTO executions (id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at,
                        priority, pool, created_at, timeout_ms)
VALUES (sqlc.arg(id), sqlc.arg(run_id), sqlc.arg(step_instance_id), sqlc.arg(attempt_no),
        sqlc.arg(attempt_id), 'queued', sqlc.arg(scheduled_at), sqlc.arg(priority), sqlc.arg(pool),
        sqlc.arg(created_at), sqlc.arg(timeout_ms));

-- name: LatestAttempt :one
SELECT attempt_no, state FROM executions
WHERE step_instance_id = sqlc.arg(step_instance_id) ORDER BY attempt_no DESC LIMIT 1;

-- name: GetExecution :one
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms
FROM executions WHERE id = sqlc.arg(id);

-- name: GetExecutionByAttempt :one
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms
FROM executions WHERE attempt_id = sqlc.arg(attempt_id);

-- name: ListExecutionsByRun :many
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms
FROM executions WHERE run_id = sqlc.arg(run_id) ORDER BY step_instance_id, attempt_no;

-- name: ClaimExecutionCAS :one
UPDATE executions SET state = 'dispatched', attempt_id = sqlc.arg(attempt_id), agent_id = sqlc.arg(agent_id),
       controller_id = sqlc.arg(controller_id), lease_expires_at = sqlc.arg(lease_expires_at)
WHERE id = sqlc.arg(id) AND state = 'queued'
RETURNING id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
          agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms;

-- name: MarkExecutionRunningCAS :execrows
UPDATE executions SET state = 'running', started_at = sqlc.arg(started_at)
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state = 'dispatched';

-- name: MarkExecutionLostCAS :one
UPDATE executions SET state = 'lost', finished_at = sqlc.arg(finished_at), error = 'lease expired'
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state IN ('dispatched', 'running')
  AND lease_expires_at < sqlc.arg(now)
RETURNING id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
          agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms;

-- name: CompleteExecutionCAS :execrows
UPDATE executions SET state = sqlc.arg(state), outputs = sqlc.arg(outputs), error = sqlc.arg(error),
       finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state IN ('dispatched', 'running');

-- name: RequeueExecutionCAS :execrows
UPDATE executions SET state = 'queued', scheduled_at = sqlc.arg(scheduled_at), agent_id = NULL,
       controller_id = NULL, lease_expires_at = NULL
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state = 'dispatched'
  AND cancel_requested_at IS NULL;

-- A queued attempt was never dispatched, so run.advance may cancel it directly.
-- name: CancelQueuedExecutionCAS :execrows
UPDATE executions SET state = 'canceled', finished_at = sqlc.arg(finished_at), error = sqlc.arg(error)
WHERE id = sqlc.arg(id) AND run_id = sqlc.arg(run_id) AND state = 'queued';

-- run.advance flags a dispatched or running attempt for cancellation. It does
-- not check attempt_id: a claim between the advance's read and its apply
-- rotates the attempt, and the flag belongs to the execution row.
-- name: RequestExecutionCancelCAS :execrows
UPDATE executions SET cancel_requested_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND run_id = sqlc.arg(run_id) AND state IN ('dispatched', 'running')
  AND cancel_requested_at IS NULL;

-- A flagged attempt that was never delivered (still dispatched) ends canceled.
-- A running one only ends by the agent's report or the reaper.
-- name: CancelUndeliveredCAS :one
UPDATE executions SET state = 'canceled', finished_at = sqlc.arg(finished_at), error = 'run cancelled'
WHERE id = sqlc.arg(id) AND attempt_id = sqlc.arg(attempt_id) AND state = 'dispatched'
  AND cancel_requested_at IS NOT NULL
RETURNING id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
          agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms;

-- name: ListCancelRequested :many
SELECT id, run_id, step_instance_id, attempt_no, attempt_id, state, scheduled_at, priority, pool,
       agent_id, controller_id, lease_expires_at, outputs, error, created_at, started_at, finished_at, cancel_requested_at, timeout_ms
FROM executions
WHERE controller_id = sqlc.arg(controller_id) AND state IN ('dispatched', 'running')
  AND cancel_requested_at IS NOT NULL AND id > sqlc.arg(after_id)
ORDER BY id LIMIT sqlc.arg(row_limit);
