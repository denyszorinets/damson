-- +goose Up
-- Agent link (M1): pools, hashed pool tokens, agents and the controller
-- registry (ARCHITECTURE sections 4, 7.1, 8.5). Ids are UUIDs generated in Go.
-- Timestamps are fixed-width UTC text (2006-01-02T15:04:05.000000Z) so they sort; JSON columns are text (JSON handled in Go).
CREATE TABLE agent_pools (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);

-- token_hash is the hex SHA-256 of the token; the plaintext is never stored.
CREATE TABLE pool_tokens (
    id           TEXT PRIMARY KEY,
    pool_id      TEXT NOT NULL REFERENCES agent_pools (id),
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    revoked_at   TEXT
);
CREATE INDEX pool_tokens_pool_id_idx ON pool_tokens (pool_id);

CREATE TABLE controller_instances (
    id           TEXT PRIMARY KEY,
    internal_url TEXT NOT NULL,
    started_at   TEXT NOT NULL,
    last_seen    TEXT NOT NULL
);

-- id is the agent instance id (X-Damson-Agent-Id). controller_id is the
-- controller currently holding the socket; it has no foreign key because a
-- controller row may be deleted on shutdown while agents re-attach elsewhere.
-- session_seq is the agent's per-process connection counter from agent.hello;
-- it orders ownership changes so a delayed hello cannot steal a newer session.
CREATE TABLE agents (
    id              TEXT    PRIMARY KEY,
    pool_id         TEXT    NOT NULL REFERENCES agent_pools (id),
    version         TEXT    NOT NULL,
    protocol        INTEGER NOT NULL,
    labels          TEXT    NOT NULL DEFAULT '{}',
    capabilities    TEXT    NOT NULL DEFAULT '{}',
    capacity        INTEGER NOT NULL,
    telemetry       TEXT    NOT NULL DEFAULT '{}',
    controller_id   TEXT,
    session_seq     INTEGER   NOT NULL DEFAULT 0,
    connected_at    TEXT    NOT NULL,
    last_seen       TEXT    NOT NULL,
    disconnected_at TEXT
);
CREATE INDEX agents_pool_id_idx ON agents (pool_id);
CREATE INDEX agents_controller_id_idx ON agents (controller_id);

-- +goose Down
DROP TABLE agents;
DROP TABLE controller_instances;
DROP TABLE pool_tokens;
DROP TABLE agent_pools;
