-- +goose Up
-- The inventory (ARCHITECTURE 11): execution targets. tags and attributes are
-- JSON text encoded and decoded in Go. Enumerations (transport, host key
-- policy) are validated in Go, in one place, because SQLite cannot change a
-- CHECK without rebuilding the table. name is compared bytewise (COLLATE "C")
-- so keyset order is the same as on SQLite; names that differ only in case are
-- refused by the lower(name) index. host_key_observed* is written by the host
-- key TOFU flow without touching version.
CREATE TABLE nodes (
    id                   TEXT        PRIMARY KEY,
    name                 TEXT        COLLATE "C" NOT NULL UNIQUE,
    address              TEXT        NOT NULL,
    port                 INTEGER     NOT NULL,
    transport            TEXT        NOT NULL,
    os                   TEXT        NOT NULL DEFAULT '',
    pool                 TEXT        NOT NULL DEFAULT '',
    tags                 TEXT        NOT NULL DEFAULT '[]',
    attributes           TEXT        NOT NULL DEFAULT '{}',
    credential           TEXT        NOT NULL DEFAULT '',
    host_key             TEXT        NOT NULL DEFAULT '',
    host_key_policy      TEXT        NOT NULL DEFAULT 'strict',
    host_key_observed    TEXT        NOT NULL DEFAULT '',
    host_key_observed_at TIMESTAMPTZ NULL,
    source               TEXT        NOT NULL,
    external_id          TEXT        NOT NULL DEFAULT '',
    enabled              BOOLEAN     NOT NULL DEFAULT TRUE,
    version              BIGINT      NOT NULL DEFAULT 1,
    created_at           TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX nodes_name_lower ON nodes (lower(name));
CREATE UNIQUE INDEX nodes_source_external_id ON nodes (source, external_id) WHERE external_id <> '';
CREATE INDEX nodes_source_name ON nodes (source, name);

-- +goose Down
DROP TABLE nodes;
