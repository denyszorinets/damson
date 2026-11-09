-- +goose Up
-- Run retention (ADR-0074) lists the oldest finished runs in every batch.
CREATE INDEX runs_finished_idx ON runs (finished_at, id) WHERE status IN ('succeeded', 'failed', 'canceled');

-- +goose Down
DROP INDEX runs_finished_idx;
