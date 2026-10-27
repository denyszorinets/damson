-- name: UpsertAgent :exec
INSERT INTO agents (id, pool_id, version, protocol, labels, capabilities, capacity, telemetry,
                    controller_id, connected_at, last_seen, disconnected_at)
VALUES (sqlc.arg(id), sqlc.arg(pool_id), sqlc.arg(version), sqlc.arg(protocol), sqlc.arg(labels),
        sqlc.arg(capabilities), sqlc.arg(capacity), sqlc.arg(telemetry),
        sqlc.arg(controller_id), sqlc.arg(connected_at), sqlc.arg(last_seen), NULL)
ON CONFLICT (id) DO UPDATE SET
    pool_id = excluded.pool_id,
    version = excluded.version,
    protocol = excluded.protocol,
    labels = excluded.labels,
    capabilities = excluded.capabilities,
    capacity = excluded.capacity,
    telemetry = excluded.telemetry,
    controller_id = excluded.controller_id,
    connected_at = excluded.connected_at,
    last_seen = excluded.last_seen,
    disconnected_at = NULL;

-- name: TouchAgentCAS :execrows
UPDATE agents SET last_seen = sqlc.arg(last_seen), telemetry = sqlc.arg(telemetry)
WHERE id = sqlc.arg(id) AND controller_id = sqlc.arg(controller_id);

-- name: DisconnectAgentCAS :execrows
UPDATE agents SET disconnected_at = sqlc.arg(disconnected_at), controller_id = NULL
WHERE id = sqlc.arg(id) AND controller_id = sqlc.arg(controller_id);

-- name: GetAgent :one
SELECT id, pool_id, version, protocol, labels, capabilities, capacity, telemetry,
       controller_id, connected_at, last_seen, disconnected_at
FROM agents
WHERE id = sqlc.arg(id);

-- name: ListAgentsAfter :many
SELECT id, pool_id, version, protocol, labels, capabilities, capacity, telemetry,
       controller_id, connected_at, last_seen, disconnected_at
FROM agents
WHERE id > sqlc.arg(after_id)
ORDER BY id
LIMIT sqlc.arg(row_limit);
