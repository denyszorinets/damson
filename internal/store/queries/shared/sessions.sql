-- name: InsertOIDCLogin :exec
INSERT INTO oidc_logins (state_hash, binding_hash, nonce, verifier, return_to, expires_at)
VALUES (sqlc.arg(state_hash), sqlc.arg(binding_hash), sqlc.arg(nonce), sqlc.arg(verifier), sqlc.arg(return_to), sqlc.arg(expires_at));

-- name: ConsumeOIDCLogin :one
-- Deletes and returns the login: a state can be used once, and not after it expired.
DELETE FROM oidc_logins WHERE state_hash = sqlc.arg(state_hash) AND expires_at > sqlc.arg(now)
RETURNING state_hash, binding_hash, nonce, verifier, return_to, expires_at;

-- name: InsertSession :exec
INSERT INTO sessions (id, token_hash, sub, email, name, claims, instance_admin, created_at, last_seen_at, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(token_hash), sqlc.arg(sub), sqlc.arg(email), sqlc.arg(name), sqlc.arg(claims),
        sqlc.arg(instance_admin), sqlc.arg(created_at), sqlc.arg(created_at), sqlc.arg(expires_at));

-- name: FindSession :one
-- The session of the token hash, if it has not expired and was seen since idle_cutoff.
SELECT id, token_hash, sub, email, name, claims, instance_admin, created_at, last_seen_at, expires_at
FROM sessions
WHERE token_hash = sqlc.arg(token_hash) AND expires_at > sqlc.arg(now) AND last_seen_at > sqlc.arg(idle_cutoff);

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = sqlc.arg(now) WHERE id = sqlc.arg(id) AND last_seen_at < sqlc.arg(now);

-- name: DeleteSessionByHash :execrows
DELETE FROM sessions WHERE token_hash = sqlc.arg(token_hash);

-- name: PurgeSessions :execrows
DELETE FROM sessions WHERE expires_at <= sqlc.arg(now) OR last_seen_at <= sqlc.arg(idle_cutoff);

-- name: PurgeOIDCLogins :execrows
DELETE FROM oidc_logins WHERE expires_at <= sqlc.arg(now);
