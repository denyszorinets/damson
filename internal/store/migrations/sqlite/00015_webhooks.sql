-- +goose Up
-- Webhooks (ARCHITECTURE 12.3). Only the SHA-256 of the token is stored; the
-- token is a credential shown once at creation. A revoked webhook keeps its row
-- (revoked_at) so the audit trail and last_used_at stay readable, but its token
-- no longer resolves. options is a JSON object of static options;
-- options_mapping is an optional CEL expression over the request.
CREATE TABLE webhooks (
    id              TEXT        PRIMARY KEY,
    job_id          TEXT        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    name            TEXT        NOT NULL,
    token_hash      TEXT        NOT NULL UNIQUE,
    options         TEXT        NOT NULL DEFAULT '{}',
    options_mapping TEXT        NOT NULL DEFAULT '',
    revoked_at      TEXT,
    last_used_at    TEXT,
    version         INTEGER     NOT NULL DEFAULT 1,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE UNIQUE INDEX webhooks_job_name ON webhooks (job_id, name) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE webhooks;
