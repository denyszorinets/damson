-- name: InsertLogChunk :execrows
INSERT INTO log_chunks (id, execution_id, seq_from, seq_to, backend, object_key, bytes, ts_first, ts_last, created_at)
VALUES (sqlc.arg(id), sqlc.arg(execution_id), sqlc.arg(seq_from), sqlc.arg(seq_to), sqlc.arg(backend),
        sqlc.arg(object_key), sqlc.arg(bytes), sqlc.arg(ts_first), sqlc.arg(ts_last), sqlc.arg(created_at));

-- Chunks overlapping [from_seq, to_seq], in sequence order.
-- name: ListLogChunks :many
SELECT id, execution_id, seq_from, seq_to, backend, object_key, bytes, ts_first, ts_last, created_at
FROM log_chunks
WHERE execution_id = sqlc.arg(execution_id) AND seq_to >= sqlc.arg(from_seq) AND seq_from <= sqlc.arg(to_seq)
ORDER BY seq_from
LIMIT sqlc.arg(row_limit);

-- name: PutLogData :exec
INSERT INTO log_chunks_data (object_key, data, created_at)
VALUES (sqlc.arg(object_key), sqlc.arg(data), sqlc.arg(created_at))
ON CONFLICT (object_key) DO UPDATE SET data = excluded.data;

-- name: GetLogData :one
SELECT data FROM log_chunks_data WHERE object_key = sqlc.arg(object_key);

-- name: CountExternalLogChunks :one
SELECT count(*) FROM log_chunks WHERE execution_id = sqlc.arg(execution_id) AND backend <> 'db';

-- name: DeleteLogChunksOfExecution :execrows
DELETE FROM log_chunks WHERE execution_id = sqlc.arg(execution_id);

-- substr is used instead of LIKE: SQLite LIKE is case-insensitive.
-- name: DeleteLogDataPrefix :execrows
DELETE FROM log_chunks_data WHERE substr(object_key, 1, sqlc.arg(prefix_len)) = sqlc.arg(prefix);

-- name: InsertLogObjectDelete :exec
INSERT INTO log_object_deletes (id, prefix, not_before, created_at)
VALUES (sqlc.arg(id), sqlc.arg(prefix), sqlc.arg(not_before), sqlc.arg(created_at));

-- name: ListDueLogObjectDeletes :many
SELECT id, prefix, not_before, tries FROM log_object_deletes
WHERE not_before <= sqlc.arg(now) ORDER BY not_before, id LIMIT sqlc.arg(row_limit);

-- name: DeferLogObjectDeleteCAS :execrows
UPDATE log_object_deletes SET not_before = sqlc.arg(next), tries = tries + 1
WHERE id = sqlc.arg(id) AND not_before = sqlc.arg(seen);

-- name: DeleteLogObjectDeleteCAS :execrows
DELETE FROM log_object_deletes WHERE id = sqlc.arg(id) AND not_before = sqlc.arg(seen);

-- name: CountLogObjectDeletes :one
SELECT count(*) FROM log_object_deletes;
