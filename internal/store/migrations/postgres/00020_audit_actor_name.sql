-- +goose Up
-- Audit actors (ARCHITECTURE 12.4): actor_name is a display snapshot (the user's
-- email), and project_path is indexed so a project admin can read the events of
-- their subtree. The subtree range needs bytewise comparison.
ALTER TABLE audit_events ADD COLUMN actor_name TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_events ALTER COLUMN project_path TYPE TEXT COLLATE "C";
CREATE INDEX audit_events_project ON audit_events (project_path, at, id);

-- +goose Down
DROP INDEX audit_events_project;
ALTER TABLE audit_events DROP COLUMN actor_name;
