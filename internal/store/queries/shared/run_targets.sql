-- name: InsertRunTarget :exec
INSERT INTO run_targets (run_id, position, name, node_id, node)
VALUES (sqlc.arg(run_id), sqlc.arg(position), sqlc.arg(name), sqlc.arg(node_id), sqlc.arg(node));

-- name: ListRunTargets :many
SELECT position, name, node_id, node FROM run_targets
WHERE run_id = sqlc.arg(run_id)
ORDER BY position;

-- name: ListRunTargetsPage :many
SELECT position, name, node_id, node FROM run_targets
WHERE run_id = sqlc.arg(run_id) AND position > sqlc.arg(after_position)
ORDER BY position
LIMIT sqlc.arg(row_limit);

-- name: GetRunTargetNodeID :one
SELECT node_id FROM run_targets WHERE run_id = sqlc.arg(run_id) AND name = sqlc.arg(name);
