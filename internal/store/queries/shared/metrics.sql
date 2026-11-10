-- name: CountRunsByStatus :many
SELECT status, count(*) AS n FROM runs GROUP BY status;

-- name: CountActiveExecutionsByPool :many
SELECT pool, state, count(*) AS n FROM executions
WHERE state IN ('queued', 'dispatched', 'running') GROUP BY pool, state;

-- name: CountConnectedAgentsByPool :many
SELECT p.name AS pool, count(*) AS agents, CAST(COALESCE(SUM(a.capacity), 0) AS BIGINT) AS slots
FROM agents a JOIN agent_pools p ON p.id = a.pool_id
-- Connected and seen recently: a crashed controller's agents age out.
WHERE a.controller_id IS NOT NULL AND a.disconnected_at IS NULL AND a.last_seen >= sqlc.arg(seen_after)
GROUP BY p.name;
