-- name: InsertSchedule :exec
INSERT INTO schedules (id, job_id, name, cron, timezone, options, enabled, misfire, next_fire_at, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(job_id), sqlc.arg(name), sqlc.arg(cron), sqlc.arg(timezone), sqlc.arg(options),
        sqlc.arg(enabled), sqlc.arg(misfire), sqlc.arg(next_fire_at), 1, sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: GetSchedule :one
SELECT id, job_id, name, cron, timezone, options, enabled, misfire, next_fire_at, last_fire_at, last_run_id, last_error, version, created_at, updated_at
FROM schedules WHERE id = sqlc.arg(id);

-- name: ListSchedules :many
-- job_id = '' lists every schedule. The keyset is the id of the last row.
SELECT id, job_id, name, cron, timezone, options, enabled, misfire, next_fire_at, last_fire_at, last_run_id, last_error, version, created_at, updated_at
FROM schedules
WHERE (CAST(sqlc.arg(job_id) AS TEXT) = '' OR job_id = sqlc.arg(job_id))
  AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: UpdateScheduleCAS :execrows
UPDATE schedules SET name = sqlc.arg(name), cron = sqlc.arg(cron), timezone = sqlc.arg(timezone), options = sqlc.arg(options),
    enabled = sqlc.arg(enabled), misfire = sqlc.arg(misfire), next_fire_at = sqlc.arg(next_fire_at),
    version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE id = sqlc.arg(id);

-- name: ListDueSchedules :many
SELECT id, job_id, name, cron, timezone, options, enabled, misfire, next_fire_at, last_fire_at, last_run_id, last_error, version, created_at, updated_at
FROM schedules
WHERE enabled = sqlc.arg(enabled) AND next_fire_at IS NOT NULL AND next_fire_at <= sqlc.arg(now)
ORDER BY next_fire_at, id
LIMIT sqlc.arg(row_limit);

-- name: FireScheduleCAS :execrows
-- Moves a due schedule on. The WHERE on next_fire_at is the exactly-once guard.
-- The version check makes it a CAS on the whole row the scheduler read, so an
-- edit made meanwhile (options, misfire) cancels the fire. A skipped or failed
-- fire keeps the last real run.
UPDATE schedules SET next_fire_at = sqlc.arg(next_fire_at), last_fire_at = COALESCE(sqlc.narg(last_fire_at), last_fire_at),
    last_run_id = CASE WHEN sqlc.arg(last_run_id) = '' THEN last_run_id ELSE sqlc.arg(last_run_id) END,
    last_error = sqlc.arg(last_error), version = version + 1
WHERE id = sqlc.arg(id) AND enabled = sqlc.arg(enabled) AND next_fire_at = sqlc.arg(expected_fire_at) AND version = sqlc.arg(expected_version);
