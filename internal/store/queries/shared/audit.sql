-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, at, actor, action, target_type, target, project_path, details, request_id)
VALUES (sqlc.arg(id), sqlc.arg(at), sqlc.arg(actor), sqlc.arg(action), sqlc.arg(target_type), sqlc.arg(target),
        sqlc.arg(project_path), sqlc.arg(details), sqlc.arg(request_id));

-- name: ListAuditEvents :many
-- Newest first. The keyset is (at, id) of the last row of the previous page;
-- before_id = '' with a far-future before_at starts at the top.
SELECT id, at, actor, action, target_type, target, project_path, details, request_id
FROM audit_events
WHERE (CAST(sqlc.arg(target_type) AS TEXT) = '' OR target_type = sqlc.arg(target_type))
  AND (CAST(sqlc.arg(target) AS TEXT) = '' OR target = sqlc.arg(target))
  AND (at < sqlc.arg(before_at) OR (at = sqlc.arg(before_at) AND id < sqlc.arg(before_id)))
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(row_limit);
