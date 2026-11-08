-- +goose Up
-- Project node scope (ARCHITECTURE 12.1, ADR-0066): a CEL expression over a node
-- that must hold for a project to use it; empty means every node. A project's
-- effective scope is the AND of its own and its ancestors'. Compiled in Go.
ALTER TABLE projects ADD COLUMN node_scope TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE projects DROP COLUMN node_scope;
