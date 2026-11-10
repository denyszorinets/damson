-- Cheap change markers for streams that poll (SSE): a handful of indexed
-- counts instead of loading a run tree or a chunk list.

-- name: GetRunStamp :one
SELECT r.version AS version,
       (SELECT COUNT(*) FROM step_instances s WHERE s.run_id = r.id) AS steps,
       (SELECT COUNT(*) FROM step_instances s WHERE s.run_id = r.id AND s.status <> 'pending') AS steps_done,
       (SELECT COUNT(*) FROM executions e WHERE e.run_id = r.id) AS execs,
       (SELECT COUNT(*) FROM executions e WHERE e.run_id = r.id
          AND e.state IN ('succeeded', 'failed', 'canceled', 'timed_out', 'lost')) AS execs_done,
       (SELECT COUNT(*) FROM executions e WHERE e.run_id = r.id AND e.state = 'running') AS execs_running,
       (SELECT COUNT(*) FROM executions e WHERE e.run_id = r.id AND e.state = 'dispatched') AS execs_dispatched
FROM runs r WHERE r.id = sqlc.arg(run_id);

-- name: GetLogStamp :one
SELECT e.state AS state,
       CAST((SELECT COUNT(*) FROM log_chunks c WHERE c.execution_id = e.id) AS BIGINT) AS chunks,
       CAST((SELECT COALESCE(MAX(c.seq_to), 0) FROM log_chunks c WHERE c.execution_id = e.id) AS BIGINT) AS max_seq
FROM executions e WHERE e.id = sqlc.arg(execution_id);
