package main

import (
	"fmt"
	"log/slog"

	"github.com/denyszorinets/damson/internal/devnodes"
)

// loadDevNodes loads the --dev-nodes file, or returns nil without a path. It
// seeds it at start (ADR-0044).
func loadDevNodes(path string, log *slog.Logger) (*devnodes.File, error) {
	if path == "" {
		return nil, nil
	}
	f, err := devnodes.Load(path)
	if err != nil {
		return nil, fmt.Errorf("loading --dev-nodes: %w", err)
	}
	log.Info("dev nodes file loaded", "path", path, "nodes", f.Names())
	return f, nil
}
