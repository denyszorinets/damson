-- name: GetJobAccess :one
SELECT job_id, rules, version, updated_at FROM job_access WHERE job_id = sqlc.arg(job_id);

-- name: InsertJobAccess :exec
INSERT INTO job_access (job_id, rules, version, updated_at)
VALUES (sqlc.arg(job_id), sqlc.arg(rules), 1, sqlc.arg(updated_at));

-- name: UpdateJobAccessCAS :execrows
UPDATE job_access SET rules = sqlc.arg(rules), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE job_id = sqlc.arg(job_id) AND version = sqlc.arg(expected_version);

-- name: ListJobRuleRows :many
-- Jobs that carry rules of their own, with the project they sit in. Empty lists
-- and archived jobs are skipped; ordered so a bounded read is stable.
SELECT ja.job_id, j.name, j.project_id, ja.rules
FROM job_access ja JOIN jobs j ON j.id = ja.job_id
WHERE ja.rules <> '[]' AND j.archived_at IS NULL
ORDER BY ja.job_id
LIMIT sqlc.arg(row_limit);
