-- +goose Up
-- Foundation schema (M0). instance_settings is the first table; it backs the
-- store's CAS/sentinel-error contract until the domain tables arrive.
CREATE TABLE instance_settings (
    setting_key   TEXT   PRIMARY KEY,
    setting_value TEXT   NOT NULL,
    version       BIGINT NOT NULL DEFAULT 1
);

-- +goose Down
DROP TABLE instance_settings;
