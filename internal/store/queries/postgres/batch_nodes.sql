-- name: GetNodesByIDs :many
SELECT * FROM nodes WHERE id = ANY(sqlc.arg(ids)::text[]);

-- name: GetNodesByNames :many
SELECT * FROM nodes WHERE name = ANY(sqlc.arg(names)::text[]);

-- name: ListRunTargetNodeIDs :many
SELECT name, node_id FROM run_targets WHERE run_id = sqlc.arg(run_id) AND name = ANY(sqlc.arg(names)::text[]);
