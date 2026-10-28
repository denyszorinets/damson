-- name: GetProjectByName :one
SELECT id, name, created_at FROM projects WHERE name = sqlc.arg(name);

-- name: ListProjects :many
SELECT id, name, created_at FROM projects ORDER BY name;

-- name: InsertJob :execrows
INSERT INTO jobs (id, project_id, name, managed_by, current_version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(project_id), sqlc.arg(name), sqlc.arg(managed_by),
        sqlc.arg(current_version), sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: InsertJobVersion :execrows
INSERT INTO job_versions (id, job_id, version, spec, checksum, created_at)
VALUES (sqlc.arg(id), sqlc.arg(job_id), sqlc.arg(version), sqlc.arg(spec), sqlc.arg(checksum), sqlc.arg(created_at));

-- name: SetJobVersionCAS :execrows
UPDATE jobs SET current_version = sqlc.arg(new_version), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND current_version = sqlc.arg(expected_version);

-- name: GetJob :one
SELECT id, project_id, name, managed_by, current_version, created_at, updated_at
FROM jobs WHERE id = sqlc.arg(id);

-- name: GetJobByName :one
SELECT id, project_id, name, managed_by, current_version, created_at, updated_at
FROM jobs WHERE project_id = sqlc.arg(project_id) AND name = sqlc.arg(name);

-- name: ListJobsAfter :many
SELECT id, project_id, name, managed_by, current_version, created_at, updated_at
FROM jobs
WHERE project_id = sqlc.arg(project_id) AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: GetJobVersion :one
SELECT id, job_id, version, spec, checksum, created_at FROM job_versions WHERE id = sqlc.arg(id);

-- name: GetJobVersionByNumber :one
SELECT id, job_id, version, spec, checksum, created_at
FROM job_versions WHERE job_id = sqlc.arg(job_id) AND version = sqlc.arg(version);

-- name: ListJobVersionsAfter :many
SELECT id, job_id, version, spec, checksum, created_at
FROM job_versions
WHERE job_id = sqlc.arg(job_id) AND version > sqlc.arg(after_version)
ORDER BY version
LIMIT sqlc.arg(row_limit);
