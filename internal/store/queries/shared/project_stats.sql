-- name: CountRunsByProject :many
-- Runs that failed since the cutoff and runs running now, by project and status,
-- for the tree's roll-ups. Archived jobs are left out.
SELECT j.project_id, r.status, count(*) AS n
FROM runs r JOIN jobs j ON j.id = r.job_id
WHERE j.archived_at IS NULL
  AND ((r.status = 'failed' AND r.finished_at >= sqlc.arg(since_at)) OR r.status = 'running')
GROUP BY j.project_id, r.status;

-- name: CountJobsByProject :many
SELECT project_id, count(*) AS n FROM jobs WHERE archived_at IS NULL GROUP BY project_id;
