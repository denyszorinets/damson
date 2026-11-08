-- name: ListApplyJobsByProject :many
SELECT j.id, j.project_id, p.path AS project_path, j.name, j.managed_by, j.current_version, j.archived_at, v.checksum,
       (SELECT count(*) FROM schedules s WHERE s.job_id = j.id AND s.enabled)
     + (SELECT count(*) FROM webhooks w WHERE w.job_id = j.id AND w.revoked_at IS NULL) AS bindings
FROM jobs j
JOIN projects p ON p.id = j.project_id
JOIN job_versions v ON v.job_id = j.id AND v.version = j.current_version
WHERE j.project_id = sqlc.arg(project_id)
ORDER BY j.name;

-- name: ListApplyJobsBySource :many
SELECT j.id, j.project_id, p.path AS project_path, j.name, j.managed_by, j.current_version, j.archived_at, v.checksum,
       (SELECT count(*) FROM schedules s WHERE s.job_id = j.id AND s.enabled)
     + (SELECT count(*) FROM webhooks w WHERE w.job_id = j.id AND w.revoked_at IS NULL) AS bindings
FROM jobs j
JOIN projects p ON p.id = j.project_id
JOIN job_versions v ON v.job_id = j.id AND v.version = j.current_version
WHERE j.managed_by = sqlc.arg(managed_by) AND j.managed_by <> 'ui'
ORDER BY p.path, j.name;

-- name: InsertManagedProject :exec
INSERT INTO projects (id, path, parent_id, description, version, created_at, updated_at, access_rules, node_scope, managed_by)
VALUES (sqlc.arg(id), sqlc.arg(path), sqlc.narg(parent_id), sqlc.arg(description), 1, sqlc.arg(now), sqlc.arg(now),
        sqlc.arg(access_rules), sqlc.arg(node_scope), sqlc.arg(managed_by));

-- name: ApplyProjectUpdateCAS :execrows
UPDATE projects SET description = sqlc.arg(description), access_rules = sqlc.arg(access_rules), node_scope = sqlc.arg(node_scope),
       managed_by = sqlc.arg(new_managed_by), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version) AND managed_by = sqlc.arg(expected_managed_by);

-- name: ApplyJobUpdateCAS :execrows
UPDATE jobs SET current_version = sqlc.arg(new_version), managed_by = sqlc.arg(new_managed_by), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND current_version = sqlc.arg(expected_version) AND managed_by = sqlc.arg(expected_managed_by)
  AND archived_at IS NULL;

-- name: ApplyJobRestoreCAS :execrows
UPDATE jobs SET current_version = sqlc.arg(new_version), managed_by = sqlc.arg(new_managed_by), updated_at = sqlc.arg(updated_at),
       archived_at = NULL
WHERE id = sqlc.arg(id) AND current_version = sqlc.arg(expected_version) AND managed_by = sqlc.arg(expected_managed_by)
  AND archived_at IS NOT NULL;

-- name: ApplyJobArchiveCAS :execrows
-- A schedule or webhook created since the plan keeps the job alive: no row changes.
UPDATE jobs SET archived_at = sqlc.arg(archived_at), updated_at = sqlc.arg(archived_at)
WHERE jobs.id = sqlc.arg(id) AND jobs.current_version = sqlc.arg(expected_version) AND jobs.managed_by = sqlc.arg(expected_managed_by)
  AND jobs.archived_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM schedules s WHERE s.job_id = jobs.id AND s.enabled)
  AND NOT EXISTS (SELECT 1 FROM webhooks w WHERE w.job_id = jobs.id AND w.revoked_at IS NULL);
