-- name: InsertNode :exec
INSERT INTO nodes (id, name, address, port, transport, os, pool, tags, attributes, credential, host_key,
                   host_key_policy, source, external_id, enabled, version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(address), sqlc.arg(port), sqlc.arg(transport), sqlc.arg(os),
        sqlc.arg(pool), sqlc.arg(tags), sqlc.arg(attributes), sqlc.arg(credential), sqlc.arg(host_key),
        sqlc.arg(host_key_policy), sqlc.arg(source), sqlc.arg(external_id), sqlc.arg(enabled), 1,
        sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: GetNodeByName :one
SELECT * FROM nodes WHERE name = sqlc.arg(name);

-- name: GetNodeByLowerName :one
-- Names are unique case-insensitively (the lower(name) index).
SELECT * FROM nodes WHERE lower(name) = lower(sqlc.arg(name));

-- name: GetNodeByID :one
SELECT * FROM nodes WHERE id = sqlc.arg(id);

-- name: ListNodes :many
SELECT * FROM nodes
WHERE name > sqlc.arg(after_name)
  AND (CAST(sqlc.arg(source) AS TEXT) = '' OR source = sqlc.arg(source))
  AND (CAST(sqlc.arg(any_enabled) AS INTEGER) = 1 OR enabled = sqlc.arg(enabled))
ORDER BY name
LIMIT sqlc.arg(row_limit);

-- name: UpdateNodeCAS :execrows
-- Never writes source, external_id, created_at or the observed host key.
UPDATE nodes SET
    address = sqlc.arg(address), port = sqlc.arg(port), transport = sqlc.arg(transport), os = sqlc.arg(os),
    pool = sqlc.arg(pool), tags = sqlc.arg(tags), attributes = sqlc.arg(attributes),
    credential = sqlc.arg(credential), host_key = sqlc.arg(host_key), host_key_policy = sqlc.arg(host_key_policy),
    enabled = sqlc.arg(enabled), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);

-- name: DeleteNodeCAS :execrows
DELETE FROM nodes WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);

-- name: UpdateNodeSyncCAS :execrows
-- A sync owns everything a source describes, including the name and the
-- external id (a rename). It never writes source, created_at or the observed
-- host key.
UPDATE nodes SET
    name = sqlc.arg(name), external_id = sqlc.arg(external_id),
    address = sqlc.arg(address), port = sqlc.arg(port), transport = sqlc.arg(transport), os = sqlc.arg(os),
    pool = sqlc.arg(pool), tags = sqlc.arg(tags), attributes = sqlc.arg(attributes),
    credential = sqlc.arg(credential), host_key = sqlc.arg(host_key), host_key_policy = sqlc.arg(host_key_policy),
    enabled = sqlc.arg(enabled), version = version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND version = sqlc.arg(expected_version);

-- name: ObserveNodeHostKey :execrows
-- Records the first key a tofu node showed, if the reporting attempt is still
-- current and unfinished. It never overwrites a key and never touches a node
-- that is pinned or not tofu.
UPDATE nodes SET host_key_observed = sqlc.arg(host_key_observed), host_key_observed_at = sqlc.arg(observed_at)
WHERE nodes.id = sqlc.arg(id) AND host_key_policy = 'tofu' AND host_key = '' AND host_key_observed = ''
  AND EXISTS (SELECT 1 FROM executions e WHERE e.attempt_id = sqlc.arg(attempt_id) AND e.agent_id = sqlc.arg(agent_id)
              AND e.state IN ('dispatched', 'running'));

-- name: ClearNodeHostKeyObserved :execrows
UPDATE nodes SET host_key_observed = '', host_key_observed_at = NULL WHERE id = sqlc.arg(id);
