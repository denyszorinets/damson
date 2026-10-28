-- name: InsertRun :execrows
INSERT INTO runs (id, job_id, job_version_id, trigger, options, status, version, concurrency_key, created_at)
VALUES (sqlc.arg(id), sqlc.arg(job_id), sqlc.arg(job_version_id), sqlc.arg(trigger), sqlc.arg(options),
        sqlc.arg(status), 1, sqlc.arg(concurrency_key), sqlc.arg(created_at));

-- name: GetRun :one
SELECT id, job_id, job_version_id, trigger, options, status, version, cancel_requested_at,
       concurrency_key, created_at, started_at, finished_at
FROM runs WHERE id = sqlc.arg(id);

-- An empty before_id selects the first (newest) page.
-- name: ListRunsBefore :many
SELECT id, job_id, job_version_id, trigger, options, status, version, cancel_requested_at,
       concurrency_key, created_at, started_at, finished_at
FROM runs
WHERE job_id = sqlc.arg(job_id) AND (sqlc.arg(before_id) = '' OR id < sqlc.arg(before_id))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- Empty job_id, status and before_id select every job, every status and the
-- newest page.
-- name: QueryRunsBefore :many
SELECT id, job_id, job_version_id, trigger, options, status, version, cancel_requested_at,
       concurrency_key, created_at, started_at, finished_at
FROM runs
WHERE (sqlc.arg(job_id) = '' OR job_id = sqlc.arg(job_id))
  AND (sqlc.arg(status) = '' OR status = sqlc.arg(status))
  AND (sqlc.arg(before_id) = '' OR id < sqlc.arg(before_id))
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);

-- name: UpdateRunStatusCAS :execrows
UPDATE runs SET status = sqlc.arg(status), version = version + 1,
       started_at = sqlc.arg(started_at), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version)
  AND status NOT IN ('succeeded', 'failed', 'canceled');

-- name: BumpRunVersionCAS :execrows
UPDATE runs SET version = version + 1
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version)
  AND status NOT IN ('succeeded', 'failed', 'canceled');

-- name: RequestRunCancelCAS :execrows
UPDATE runs SET cancel_requested_at = sqlc.arg(now), version = version + 1
WHERE id = sqlc.arg(id) AND cancel_requested_at IS NULL
  AND status NOT IN ('succeeded', 'failed', 'canceled');

-- name: InsertStepInstance :execrows
INSERT INTO step_instances (id, run_id, path, kind, plugin, input, status, created_at)
VALUES (sqlc.arg(id), sqlc.arg(run_id), sqlc.arg(path), sqlc.arg(kind), sqlc.arg(plugin),
        sqlc.arg(input), sqlc.arg(status), sqlc.arg(created_at));

-- name: GetStepInstance :one
SELECT id, run_id, path, kind, plugin, input, status, outputs, error, created_at, started_at, finished_at
FROM step_instances WHERE id = sqlc.arg(id);

-- name: ListStepInstancesByRun :many
SELECT id, run_id, path, kind, plugin, input, status, outputs, error, created_at, started_at, finished_at
FROM step_instances WHERE run_id = sqlc.arg(run_id) ORDER BY id;

-- name: UpdateStepInstanceCAS :execrows
UPDATE step_instances SET status = sqlc.arg(status), outputs = sqlc.arg(outputs), error = sqlc.arg(error),
       started_at = sqlc.arg(started_at), finished_at = sqlc.arg(finished_at)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(expected_status);
