-- +goose Up
-- Jobs as code (ARCHITECTURE 13, ADR-0069). See the PostgreSQL migration.
ALTER TABLE projects ADD COLUMN managed_by TEXT NOT NULL DEFAULT 'ui';
ALTER TABLE jobs ADD COLUMN archived_at TEXT;
CREATE INDEX jobs_managed_by_idx ON jobs (managed_by) WHERE managed_by <> 'ui';

-- +goose Down
DROP INDEX jobs_managed_by_idx;
ALTER TABLE jobs DROP COLUMN archived_at;
ALTER TABLE projects DROP COLUMN managed_by;
