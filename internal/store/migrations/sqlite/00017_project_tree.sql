-- +goose Up
-- Project tree (ARCHITECTURE 12.1, ADR-0062). The old flat name becomes the
-- materialized path ('default' is a root); paths never change, so secret refs,
-- token scopes and audit project paths stay valid. parent_id is NULL for roots.
ALTER TABLE projects RENAME COLUMN name TO path;
ALTER TABLE projects ADD COLUMN parent_id   TEXT REFERENCES projects (id);
ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN version     INTEGER NOT NULL DEFAULT 1;
ALTER TABLE projects ADD COLUMN updated_at  TEXT NOT NULL DEFAULT '1970-01-01T00:00:00.000000Z';
UPDATE projects SET updated_at = created_at;
CREATE INDEX projects_parent ON projects (parent_id, path);

-- +goose Down
DROP INDEX projects_parent;
ALTER TABLE projects DROP COLUMN updated_at;
ALTER TABLE projects DROP COLUMN version;
ALTER TABLE projects DROP COLUMN description;
ALTER TABLE projects DROP COLUMN parent_id;
ALTER TABLE projects RENAME COLUMN path TO name;
