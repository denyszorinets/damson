-- name: AcquireLeaderLease :one
-- Acquire or renew: succeeds (one row) when the caller already holds the lease
-- or the current one has expired; a takeover bumps the term.
INSERT INTO leader_leases (name, holder, term, acquired_at, expires_at)
VALUES (sqlc.arg(name), sqlc.arg(holder), 1, sqlc.arg(now), sqlc.arg(expires_at))
ON CONFLICT (name) DO UPDATE SET
    holder = excluded.holder,
    expires_at = excluded.expires_at,
    term = CASE WHEN leader_leases.holder = excluded.holder THEN leader_leases.term ELSE leader_leases.term + 1 END,
    acquired_at = CASE WHEN leader_leases.holder = excluded.holder THEN leader_leases.acquired_at ELSE excluded.acquired_at END
WHERE leader_leases.holder = excluded.holder OR leader_leases.expires_at <= excluded.acquired_at
RETURNING term, expires_at;

-- name: ReleaseLeaderLease :execrows
UPDATE leader_leases SET expires_at = sqlc.arg(now)
WHERE name = sqlc.arg(name) AND holder = sqlc.arg(holder) AND term = sqlc.arg(term) AND expires_at > sqlc.arg(now);

-- name: CheckLeaderLease :one
SELECT COUNT(*) FROM leader_leases
WHERE name = sqlc.arg(name) AND holder = sqlc.arg(holder) AND term = sqlc.arg(term) AND expires_at > sqlc.arg(now);
