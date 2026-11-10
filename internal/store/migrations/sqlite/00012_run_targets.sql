-- +goose Up
-- The nodes a run targets, frozen when the run is created (ARCHITECTURE 5.5):
-- the trigger selects them once and writes them in the run-creation
-- transaction, so the advancer and for_each read this snapshot and never the
-- live inventory. node holds the selector map as JSON (name, address, tags,
-- attributes, pool, os, transport), encoded in Go; node_id is the id of the
-- inventory row at trigger time, with no foreign key because the node may be
-- deleted while the run is still going (dispatch then fails the step). Runs
-- created before this migration have no rows, which reads as no targets.
CREATE TABLE run_targets (
    run_id   TEXT    NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT    NOT NULL,
    node_id  TEXT    NOT NULL,
    node     TEXT    NOT NULL,
    PRIMARY KEY (run_id, position),
    UNIQUE (run_id, name),
    UNIQUE (run_id, node_id)
);

-- +goose Down
DROP TABLE run_targets;
