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

-- name: LatestRunsByJobAndStatus :many
-- The newest run of each status for every job of a project (run ids are time
-- ordered), so the caller can read a job's last success, last failure and last
-- run from at most five rows per job. Archived jobs are left out.
SELECT t.job_id, t.id, t.status, t.created_at, t.started_at, t.finished_at
FROM (
  SELECT r.job_id, r.id, r.status, r.created_at, r.started_at, r.finished_at,
         ROW_NUMBER() OVER (PARTITION BY r.job_id, r.status ORDER BY r.id DESC) AS rn
  FROM runs r JOIN jobs j ON j.id = r.job_id
  WHERE j.project_id = sqlc.arg(project_id) AND j.archived_at IS NULL
) t
WHERE t.rn = 1;
