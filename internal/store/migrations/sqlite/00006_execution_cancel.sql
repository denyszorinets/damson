-- +goose Up
-- cancel_requested_at: run.advance decided that this dispatched or running
-- attempt must stop (ARCHITECTURE section 8.3). The controller that owns the
-- attempt polls for flagged rows and sends step.cancel. A queued row never
-- carries the flag: the engine cancels queued executions directly, and a
-- flagged attempt that goes back to the queue is cancelled instead.
ALTER TABLE executions ADD COLUMN cancel_requested_at TEXT;
CREATE INDEX executions_cancel_idx ON executions (controller_id)
    WHERE cancel_requested_at IS NOT NULL AND state IN ('dispatched', 'running');

-- +goose Down
DROP INDEX executions_cancel_idx;
ALTER TABLE executions DROP COLUMN cancel_requested_at;
