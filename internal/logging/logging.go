// Package logging builds the process-wide slog JSON logger.
package logging

import (
	"io"
	"log/slog"
)

// New returns a JSON logger writing to w at level. Component-specific
// attributes (run_id, execution_id, attempt_id, agent_id) are added by callers
// with Logger.With.
func New(w io.Writer, level slog.Level, role string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h).With("role", role)
}
