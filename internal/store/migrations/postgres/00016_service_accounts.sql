-- +goose Up
-- Service accounts and their API tokens (ARCHITECTURE 12.2). Only the SHA-256
-- of a token is stored; the token is shown once at creation. A token is scoped
-- by project (a path; '' means every project) and a role, may expire, and can be
-- revoked (the row stays so audit and last_used_at remain readable). Roles are
-- validated in Go, because SQLite cannot change a CHECK without a rebuild.
CREATE TABLE service_accounts (
    id          TEXT        PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    description TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE service_tokens (
    id                 TEXT        PRIMARY KEY,
    service_account_id TEXT        NOT NULL REFERENCES service_accounts (id) ON DELETE CASCADE,
    name               TEXT        NOT NULL,
    token_hash         TEXT        NOT NULL UNIQUE,
    project            TEXT        NOT NULL,
    role               TEXT        NOT NULL,
    expires_at         TIMESTAMPTZ,
    revoked_at         TIMESTAMPTZ,
    last_used_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL
);

CREATE INDEX service_tokens_account ON service_tokens (service_account_id, id);

-- +goose Down
DROP TABLE service_tokens;
DROP TABLE service_accounts;
