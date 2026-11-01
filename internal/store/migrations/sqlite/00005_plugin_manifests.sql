-- +goose Up
-- Persisted plugin manifests (ARCHITECTURE section 6): content-addressed by
-- name+version+checksum, plus a link from each agent to the plugins it
-- reported. The link is replaced in the agent's hello transaction, so it
-- follows the same session_seq ordering as the agents row. Liveness of a
-- provider is read from the agents row, never deleted on disconnect.
CREATE TABLE plugin_manifests (
    name       TEXT        NOT NULL,
    version    TEXT        NOT NULL,
    checksum   TEXT        NOT NULL,
    manifest   TEXT        NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (name, version, checksum)
);

CREATE TABLE agent_plugins (
    agent_id TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    name     TEXT NOT NULL,
    version  TEXT NOT NULL,
    checksum TEXT NOT NULL,
    PRIMARY KEY (agent_id, name, version),
    FOREIGN KEY (name, version, checksum) REFERENCES plugin_manifests (name, version, checksum)
);
CREATE INDEX agent_plugins_manifest_idx ON agent_plugins (name, version, checksum);

-- +goose Down
DROP TABLE agent_plugins;
DROP TABLE plugin_manifests;
