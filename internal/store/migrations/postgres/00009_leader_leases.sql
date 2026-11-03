-- +goose Up
-- One row per singleton duty ("reaper"). holder is a controller id, with no
-- foreign key because controller_instances rows are deleted on shutdown. term
-- grows by one each time the lease changes hands and never resets (a release
-- only expires the row), so it fences the actions of a deposed leader.
CREATE TABLE leader_leases (
    name        TEXT        PRIMARY KEY,
    holder      TEXT        NOT NULL,
    term        BIGINT      NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE leader_leases;
