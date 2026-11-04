-- +goose Up
-- Envelope-encrypted secrets of the db backend (ARCHITECTURE 9). A secret is
-- addressed by scope_path (a project path or _inventory) and name. current_version
-- is the CAS counter; each version row holds the ciphertext wrapped under
-- the KEK named by kek_id. field_names is a JSON array of the field names,
-- so listing never touches the encrypted columns.
CREATE TABLE secrets (
    id              TEXT        PRIMARY KEY,
    scope_path      TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    type            TEXT        NOT NULL,
    current_version INTEGER     NOT NULL,
    created_at      TEXT        NOT NULL,
    updated_at      TEXT        NOT NULL,
    UNIQUE (scope_path, name)
);

CREATE TABLE secret_versions (
    secret_id   TEXT        NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    version     INTEGER     NOT NULL,
    kek_id      TEXT        NOT NULL,
    wrapped_dek BLOB        NOT NULL,
    nonce       BLOB        NOT NULL,
    ciphertext  BLOB        NOT NULL,
    field_names TEXT        NOT NULL,
    created_at  TEXT        NOT NULL,
    PRIMARY KEY (secret_id, version)
);

CREATE INDEX secret_versions_kek_id ON secret_versions (kek_id);

-- +goose Down
DROP TABLE secret_versions;
DROP TABLE secrets;
