-- name: InsertSecret :exec
INSERT INTO secrets (id, scope_path, name, type, current_version, created_at, updated_at)
VALUES (sqlc.arg(id), sqlc.arg(scope_path), sqlc.arg(name), sqlc.arg(type), 1, sqlc.arg(created_at), sqlc.arg(updated_at));

-- name: InsertSecretVersion :exec
INSERT INTO secret_versions (secret_id, version, kek_id, wrapped_dek, nonce, ciphertext, field_names, created_at)
VALUES (sqlc.arg(secret_id), sqlc.arg(version), sqlc.arg(kek_id), sqlc.arg(wrapped_dek), sqlc.arg(nonce),
        sqlc.arg(ciphertext), sqlc.arg(field_names), sqlc.arg(created_at));

-- name: BumpSecretVersionCAS :execrows
UPDATE secrets SET current_version = current_version + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND current_version = sqlc.arg(expected_version);

-- name: GetSecretHead :one
SELECT id, scope_path, name, type, current_version, created_at, updated_at
FROM secrets WHERE scope_path = sqlc.arg(scope_path) AND name = sqlc.arg(name);

-- name: GetSecretHeadByID :one
SELECT id, scope_path, name, type, current_version, created_at, updated_at
FROM secrets WHERE id = sqlc.arg(id);

-- name: GetSecretCurrent :one
SELECT s.id, s.scope_path, s.name, s.type, s.current_version, s.created_at, s.updated_at,
       v.kek_id, v.wrapped_dek, v.nonce, v.ciphertext, v.field_names
FROM secrets s
JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE s.scope_path = sqlc.arg(scope_path) AND s.name = sqlc.arg(name);

-- name: ListSecretMeta :many
-- Never selects the encrypted columns.
SELECT s.id, s.scope_path, s.name, s.type, s.current_version, s.created_at, s.updated_at, v.field_names
FROM secrets s
JOIN secret_versions v ON v.secret_id = s.id AND v.version = s.current_version
WHERE (CAST(sqlc.arg(scope_filter) AS TEXT) = '' OR s.scope_path = sqlc.arg(scope_filter))
  AND (s.scope_path > sqlc.arg(after_scope) OR (s.scope_path = sqlc.arg(after_scope) AND s.name > sqlc.arg(after_name)))
ORDER BY s.scope_path, s.name
LIMIT sqlc.arg(row_limit);

-- name: PruneSecretVersions :exec
DELETE FROM secret_versions WHERE secret_id = sqlc.arg(secret_id) AND version <= sqlc.arg(max_pruned);

-- name: DeleteSecretByName :execrows
-- One statement: the row lock on secrets comes first, and the versions go with
-- it through ON DELETE CASCADE.
DELETE FROM secrets WHERE scope_path = sqlc.arg(scope_path) AND name = sqlc.arg(name);
