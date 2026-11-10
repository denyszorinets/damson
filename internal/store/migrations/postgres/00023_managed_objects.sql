-- +goose Up
-- Jobs as code (ARCHITECTURE 13, ADR-0069). jobs.managed_by already exists; projects
-- get one too ("ui" or "files:<source>"). Pruning a job archives it: runs keep
-- their job_id, so a job is never hard-deleted.
ALTER TABLE projects ADD COLUMN managed_by TEXT NOT NULL DEFAULT 'ui';
ALTER TABLE jobs ADD COLUMN archived_at TIMESTAMPTZ;
CREATE INDEX jobs_managed_by_idx ON jobs (managed_by) WHERE managed_by <> 'ui';

-- +goose Down
DROP INDEX jobs_managed_by_idx;
ALTER TABLE jobs DROP COLUMN archived_at;
ALTER TABLE projects DROP COLUMN managed_by;
