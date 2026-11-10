-- +goose Up
-- The inventory (ARCHITECTURE 11): execution targets. tags and attributes are
-- JSON text encoded and decoded in Go. Enumerations (transport, host key
-- policy) are validated in Go, in one place, because SQLite cannot change a
-- CHECK without rebuilding the table. Names that differ only in case are
-- refused by the lower(name) index. host_key_observed* is written by the host
-- key TOFU flow without touching version.
CREATE TABLE nodes (
    id                   TEXT    PRIMARY KEY,
    name                 TEXT    NOT NULL UNIQUE,
    address              TEXT    NOT NULL,
    port                 INTEGER NOT NULL,
    transport            TEXT    NOT NULL,
    os                   TEXT    NOT NULL DEFAULT '',
    pool                 TEXT    NOT NULL DEFAULT '',
    tags                 TEXT    NOT NULL DEFAULT '[]',
    attributes           TEXT    NOT NULL DEFAULT '{}',
    credential           TEXT    NOT NULL DEFAULT '',
    host_key             TEXT    NOT NULL DEFAULT '',
    host_key_policy      TEXT    NOT NULL DEFAULT 'strict',
    host_key_observed    TEXT    NOT NULL DEFAULT '',
    host_key_observed_at TEXT,
    source               TEXT    NOT NULL,
    external_id          TEXT    NOT NULL DEFAULT '',
    enabled              INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    version              INTEGER NOT NULL DEFAULT 1,
    created_at           TEXT    NOT NULL,
    updated_at           TEXT    NOT NULL
);

CREATE UNIQUE INDEX nodes_name_lower ON nodes (lower(name));
CREATE UNIQUE INDEX nodes_source_external_id ON nodes (source, external_id) WHERE external_id <> '';
CREATE INDEX nodes_source_name ON nodes (source, name);

-- +goose Down
DROP TABLE nodes;
