-- name: InsertServiceAccount :exec
INSERT INTO service_accounts (id, name, description, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(description), sqlc.arg(created_at), sqlc.arg(created_at));

-- name: GetServiceAccount :one
SELECT id, name, description, created_at, updated_at FROM service_accounts WHERE id = sqlc.arg(id);

-- name: ListServiceAccounts :many
SELECT id, name, description, created_at, updated_at FROM service_accounts
WHERE id > sqlc.arg(after_id) ORDER BY id LIMIT sqlc.arg(row_limit);

-- name: DeleteServiceAccount :execrows
DELETE FROM service_accounts WHERE id = sqlc.arg(id);

-- name: InsertServiceToken :exec
INSERT INTO service_tokens (id, service_account_id, name, token_hash, project, role, expires_at, created_at)
VALUES (sqlc.arg(id), sqlc.arg(service_account_id), sqlc.arg(name), sqlc.arg(token_hash), sqlc.arg(project),
        sqlc.arg(role), sqlc.arg(expires_at), sqlc.arg(created_at));

-- name: GetServiceToken :one
SELECT id, service_account_id, name, token_hash, project, role, expires_at, revoked_at, last_used_at, created_at
FROM service_tokens WHERE id = sqlc.arg(id);

-- name: ListServiceTokens :many
SELECT id, service_account_id, name, token_hash, project, role, expires_at, revoked_at, last_used_at, created_at
FROM service_tokens WHERE service_account_id = sqlc.arg(service_account_id) ORDER BY id;

-- name: FindActiveServiceToken :one
-- The token of the hash, if it is not revoked or expired at now.
SELECT id, service_account_id, name, token_hash, project, role, expires_at, revoked_at, last_used_at, created_at
FROM service_tokens
WHERE token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > sqlc.arg(now));

-- name: RevokeServiceToken :execrows
UPDATE service_tokens SET revoked_at = sqlc.arg(revoked_at) WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: TouchServiceToken :execrows
UPDATE service_tokens SET last_used_at = sqlc.arg(now)
WHERE id = sqlc.arg(id) AND (last_used_at IS NULL OR last_used_at < sqlc.arg(older_than));
