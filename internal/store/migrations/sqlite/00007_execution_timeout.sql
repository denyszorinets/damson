-- +goose Up
-- timeout_ms: the per-attempt step timeout in milliseconds, 0 for none. It is
-- sent to the agent in step.execute, which enforces it with a context deadline
-- (ARCHITECTURE section 5.2) and reports timed_out.
ALTER TABLE executions ADD COLUMN timeout_ms INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE executions DROP COLUMN timeout_ms;
