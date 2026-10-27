-- name: InsertPool :execrows
INSERT INTO agent_pools (id, name, description, created_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(created_at));

-- name: GetPoolByName :one
SELECT id, name, description, created_at
FROM agent_pools
WHERE name = sqlc.arg(name);

-- name: ListPools :many
SELECT id, name, description, created_at
FROM agent_pools
ORDER BY name;

-- name: InsertPoolToken :execrows
INSERT INTO pool_tokens (id, pool_id, name, token_hash, created_at)
VALUES (sqlc.arg(id), sqlc.arg(pool_id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(created_at));

-- name: LookupActivePoolToken :one
SELECT
    p.id AS pool_id, p.name AS pool_name, p.description AS pool_description, p.created_at AS pool_created_at,
    t.id AS token_id, t.name AS token_name, t.created_at AS token_created_at, t.last_used_at AS token_last_used_at
FROM pool_tokens t
JOIN agent_pools p ON p.id = t.pool_id
WHERE t.token_hash = sqlc.arg(token_hash) AND t.revoked_at IS NULL;

-- name: TouchPoolToken :execrows
UPDATE pool_tokens SET last_used_at = sqlc.arg(used_at)
WHERE id = sqlc.arg(id);

-- name: RevokePoolTokenCAS :execrows
UPDATE pool_tokens SET revoked_at = sqlc.arg(revoked_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: CountPoolTokens :one
SELECT count(*) FROM pool_tokens WHERE id = sqlc.arg(id);

-- name: ListPoolTokens :many
SELECT id, pool_id, name, created_at, last_used_at, revoked_at
FROM pool_tokens
WHERE pool_id = sqlc.arg(pool_id)
ORDER BY created_at, id;
