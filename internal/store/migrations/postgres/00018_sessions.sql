-- +goose Up
-- OIDC login state and browser sessions (ARCHITECTURE 12.2, ADR-0063). Both are
-- looked up by the SHA-256 hex of a random value held by the browser, so a copy
-- of the database cannot be used to log in. oidc_logins rows live for minutes and
-- are consumed (deleted) by the callback exactly once.
CREATE TABLE oidc_logins (
    state_hash   TEXT        PRIMARY KEY,
    binding_hash TEXT        NOT NULL,
    nonce        TEXT        NOT NULL,
    verifier     TEXT        NOT NULL,
    return_to    TEXT        NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX oidc_logins_expires ON oidc_logins (expires_at);

-- claims is the raw JSON of the id token's claims, frozen at login; instance_admin
-- is the admin_rule verdict at login.
CREATE TABLE sessions (
    id             TEXT        PRIMARY KEY,
    token_hash     TEXT        NOT NULL UNIQUE,
    sub            TEXT        NOT NULL,
    email          TEXT        NOT NULL,
    name           TEXT        NOT NULL,
    claims         TEXT        NOT NULL,
    instance_admin BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL,
    last_seen_at   TIMESTAMPTZ NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_at);
CREATE INDEX sessions_sub ON sessions (sub);

-- +goose Down
DROP TABLE sessions;
DROP TABLE oidc_logins;
