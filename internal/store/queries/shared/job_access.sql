-- name: GetJobAccess :one
SELECT job_id, rules, version, updated_at FROM job_access WHERE job_id = sqlc.arg(job_id);

-- name: InsertJobAccess :exec
INSERT INTO job_access (job_id, rules, version, updated_at)
VALUES (sqlc.arg(job_id), sqlc.arg(rules), 1, sqlc.arg(updated_at));

-- name: UpdateJobAccessCAS :execrows
UPDATE job_access SET rules = sqlc.arg(rules), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE job_id = sqlc.arg(job_id) AND version = sqlc.arg(expected_version);
