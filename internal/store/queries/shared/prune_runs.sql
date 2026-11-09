-- name: ListPrunableRuns :many
-- Finished runs older than the cutoff with no execution that can still write.
SELECT r.id FROM runs r
WHERE r.status IN ('succeeded', 'failed', 'canceled') AND r.finished_at IS NOT NULL AND r.finished_at < sqlc.arg(before_at)
  AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.run_id = r.id AND e.state IN ('queued', 'dispatched', 'running'))
ORDER BY r.finished_at, r.id
LIMIT sqlc.arg(row_limit);

-- name: ListExecutionIDsOfRun :many
SELECT id FROM executions WHERE run_id = sqlc.arg(run_id);

-- name: DeleteExecutionsOfRun :execrows
DELETE FROM executions WHERE run_id = sqlc.arg(run_id);

-- name: DeleteStepInstancesOfRun :execrows
DELETE FROM step_instances WHERE run_id = sqlc.arg(run_id);

-- name: DeleteFinishedRun :execrows
DELETE FROM runs WHERE id = sqlc.arg(id) AND status IN ('succeeded', 'failed', 'canceled');
