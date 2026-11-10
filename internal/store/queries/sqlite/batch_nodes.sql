-- name: GetNodesByIDs :many
SELECT * FROM nodes WHERE id IN (sqlc.slice('ids'));

-- name: GetNodesByNames :many
SELECT * FROM nodes WHERE name IN (sqlc.slice('names'));

-- name: ListRunTargetNodeIDs :many
SELECT name, node_id FROM run_targets WHERE run_id = sqlc.arg(run_id) AND name IN (sqlc.slice('names'));
