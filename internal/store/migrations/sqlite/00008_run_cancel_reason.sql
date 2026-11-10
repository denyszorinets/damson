-- +goose Up
-- cancel_reason: why runs.cancel_requested_at was set. Empty for a user or API
-- cancel; "timeout" when run.advance applied the job-level timeout. Written
-- only together with cancel_requested_at.
ALTER TABLE runs ADD COLUMN cancel_reason TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE runs DROP COLUMN cancel_reason;
