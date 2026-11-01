package main

import (
	"fmt"
	"log/slog"

	"github.com/denyszorinets/damson/internal/devnodes"
)

// loadDevNodes loads the --dev-nodes file, or returns nil without a path. It
// warns loudly: the file replaces inventory and secret authorization (ADR-0034).
func loadDevNodes(path string, log *slog.Logger) (*devnodes.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := devnodes.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading --dev-nodes: %w", err)
	}
	log.Warn("dev nodes: no inventory, no secret authorization; development only", "path", path, "nodes", f.Names())
	return f, nil
}
