-- name: GetProjectByPath :one
SELECT id, path, created_at, parent_id, description, version, updated_at FROM projects WHERE path = sqlc.arg(path);

-- name: GetProject :one
SELECT id, path, created_at, parent_id, description, version, updated_at FROM projects WHERE id = sqlc.arg(id);

-- name: ListProjects :many
SELECT id, path, created_at, parent_id, description, version, updated_at FROM projects ORDER BY path;

-- name: InsertProject :exec
INSERT INTO projects (id, path, parent_id, description, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(path), sqlc.narg(parent_id), sqlc.arg(description), 1, sqlc.arg(now), sqlc.arg(now));

-- name: UpdateProjectCAS :execrows
UPDATE projects SET description = sqlc.arg(description), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = sqlc.arg(id);

-- name: CountProjectChildren :one
SELECT count(*) FROM projects WHERE parent_id = sqlc.arg(id);

-- name: CountProjectJobs :one
SELECT count(*) FROM jobs WHERE project_id = sqlc.arg(id);

-- name: CountProjectSecrets :one
SELECT count(*) FROM secrets WHERE scope_path = sqlc.arg(path);

-- name: CountProjectTokens :one
SELECT count(*) FROM service_tokens WHERE project = sqlc.arg(path) AND revoked_at IS NULL;

-- name: ListProjectSubtree :many
-- The project and its descendants: '0' is the character after '/', so the range
-- holds exactly the paths that start with "<path>/".
SELECT id, path, created_at, parent_id, description, version, updated_at FROM projects
WHERE path = sqlc.arg(path) OR (path >= sqlc.arg(path_slash) AND path < sqlc.arg(path_next))
ORDER BY path;
