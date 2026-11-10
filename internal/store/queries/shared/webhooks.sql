-- name: InsertWebhook :exec
INSERT INTO webhooks (id, job_id, name, token_hash, options, options_mapping, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(job_id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(options), sqlc.arg(options_mapping),
        1, sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: GetWebhook :one
SELECT id, job_id, name, token_hash, options, options_mapping, revoked_at, last_used_at, version, created_at, updated_at
FROM webhooks WHERE id = sqlc.arg(id);

-- name: GetActiveWebhookByTokenHash :one
SELECT id, job_id, name, token_hash, options, options_mapping, revoked_at, last_used_at, version, created_at, updated_at
FROM webhooks WHERE token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL;

-- name: ListWebhooks :many
-- job_id = '' lists every webhook. The keyset is the id of the last row.
SELECT id, job_id, name, token_hash, options, options_mapping, revoked_at, last_used_at, version, created_at, updated_at
FROM webhooks
WHERE (CAST(sqlc.arg(job_id) AS TEXT) = '' OR job_id = sqlc.arg(job_id))
  AND id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: RevokeWebhook :execrows
UPDATE webhooks SET revoked_at = sqlc.arg(revoked_at), version = version + 1, updated_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: TouchWebhook :execrows
-- Counts a use of an active webhook. 0 rows: it is gone or revoked. last_used_at
-- moves at most once per throttle window.
UPDATE webhooks
SET last_used_at = CASE WHEN last_used_at IS NULL OR last_used_at < sqlc.arg(older_than) THEN sqlc.arg(now) ELSE last_used_at END
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: ListWebhooksByProject :many
-- The webhooks of the jobs of a project and of the projects below it; see
-- ListSchedulesByProject for the subtree test. The keyset is the id of the last row.
SELECT w.*
FROM webhooks w
  JOIN jobs j ON j.id = w.job_id
  JOIN projects p ON p.id = j.project_id
WHERE (p.path = CAST(sqlc.arg(project_path) AS TEXT)
       OR substr(p.path, 1, length(CAST(sqlc.arg(project_path) AS TEXT)) + 1) = CAST(sqlc.arg(project_path) AS TEXT) || '/')
  AND w.id > sqlc.arg(after_id)
ORDER BY w.id
LIMIT sqlc.arg(row_limit);
