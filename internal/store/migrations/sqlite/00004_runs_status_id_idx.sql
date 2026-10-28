-- +goose Up
-- Serves GET /runs?status=: newest runs of one status without a sort.
CREATE INDEX runs_status_id_idx ON runs (status, id);

-- +goose Down
DROP INDEX runs_status_id_idx;
