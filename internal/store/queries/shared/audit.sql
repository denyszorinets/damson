-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, at, actor, action, target_type, target, project_path, details, request_id, actor_name)
VALUES (sqlc.arg(id), sqlc.arg(at), sqlc.arg(actor), sqlc.arg(action), sqlc.arg(target_type), sqlc.arg(target),
        sqlc.arg(project_path), sqlc.arg(details), sqlc.arg(request_id), sqlc.arg(actor_name));

-- name: ListAuditEvents :many
-- Newest first. The keyset is (at, id) of the last row of the previous page;
-- before_id = '' with a far-future before_at starts at the top. from_at (inclusive)
-- and to_at (exclusive) bound the time range; a zero from_at and a far-future to_at
-- leave it open. A project selects
-- the events of that project and its descendants ("<project>/" up to "<project>0").
SELECT id, at, actor, action, target_type, target, project_path, details, request_id, actor_name
FROM audit_events
WHERE (CAST(sqlc.arg(project) AS TEXT) = '' OR project_path = sqlc.arg(project)
       OR (project_path >= sqlc.arg(project_slash) AND project_path < sqlc.arg(project_next)))
  AND (CAST(sqlc.arg(hide_secrets) AS TEXT) = '' OR action NOT LIKE 'secret.%')
  AND (CAST(sqlc.arg(target_type) AS TEXT) = '' OR target_type = sqlc.arg(target_type))
  AND (CAST(sqlc.arg(target) AS TEXT) = '' OR target = sqlc.arg(target))
  AND (CAST(sqlc.arg(action) AS TEXT) = '' OR action = sqlc.arg(action))
  AND (CAST(sqlc.arg(actor) AS TEXT) = '' OR actor = sqlc.arg(actor))
  AND (CAST(sqlc.arg(hide_system) AS TEXT) = '' OR actor NOT LIKE 'system:%')
  AND at >= sqlc.arg(from_at) AND at < sqlc.arg(to_at)
  AND (at < sqlc.arg(before_at) OR (at = sqlc.arg(before_at) AND id < sqlc.arg(before_id)))
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: PruneAuditEvents :execrows
-- Deletes up to row_limit events older than before_at, oldest first.
DELETE FROM audit_events
WHERE id IN (SELECT a.id FROM audit_events a WHERE a.at < sqlc.arg(before_at) ORDER BY a.at, a.id LIMIT sqlc.arg(row_limit));
