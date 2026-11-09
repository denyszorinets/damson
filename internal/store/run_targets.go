package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxRunTargets bounds the nodes a run can freeze (the for_each lane limit).
const MaxRunTargets = 1000

// MaxRunTargetNodeBytes bounds one frozen node's JSON (inventory validation
// allows about 21 KiB per node).
const MaxRunTargetNodeBytes = 32 << 10

// ClampTargetLimit maps a page size to [1, MaxRunTargets].
func ClampTargetLimit(n int) int {
	switch {
	case n <= 0:
		return 1
	case n > MaxRunTargets:
		return MaxRunTargets
	}
	return n
}

// RunTarget is one node of a run's frozen target list. Node is the selector
// map as JSON (an object); NodeID is the inventory row at trigger time.
type RunTarget struct {
	Position int // 1-based order of the list; set by the store
	Name     string
	NodeID   string
	Node     json.RawMessage
}

// RunTargetStore reads a run's frozen targets (they are written by CreateRun).
type RunTargetStore interface {
	// ListRunTargets returns every target of the run in order; none for a run
	// without a snapshot.
	ListRunTargets(ctx context.Context, runID string) ([]RunTarget, error)
	// ListRunTargetsPage returns up to limit targets with a position greater
	// than afterPosition.
	ListRunTargetsPage(ctx context.Context, runID string, afterPosition, limit int) ([]RunTarget, error)
	// RunTargetNodeID returns the frozen inventory id of the target called name,
	// or ErrNotFound if the name is not in the snapshot (case matters).
	RunTargetNodeID(ctx context.Context, runID, name string) (string, error)
	// RunTargetNodeIDs is RunTargetNodeID for many names at once, in chunks of
	// MaxBatch: it maps each name found in the snapshot to its node id; a name
	// outside the snapshot is absent.
	RunTargetNodeIDs(ctx context.Context, runID string, names []string) (map[string]string, error)
}

// ValidateRunTargets rejects a snapshot the store cannot hold: too many rows,
// an empty or duplicate name or id, or a node that is not a JSON object.
func ValidateRunTargets(ts []RunTarget) error {
	if len(ts) > MaxRunTargets {
		return fmt.Errorf("a run freezes at most %d targets, got %d: %w", MaxRunTargets, len(ts), ErrInvalid)
	}
	names, ids := make(map[string]bool, len(ts)), make(map[string]bool, len(ts))
	for _, t := range ts {
		switch {
		case t.Name == "" || t.NodeID == "":
			return fmt.Errorf("a target needs a name and a node id: %w", ErrInvalid)
		case !utf8.ValidString(t.Name) || !utf8.ValidString(t.NodeID) ||
			strings.ContainsRune(t.Name, 0) || strings.ContainsRune(t.NodeID, 0):
			return fmt.Errorf("a target name and node id must be text without NUL: %w", ErrInvalid)
		case len(t.Node) > MaxRunTargetNodeBytes:
			return fmt.Errorf("the node of target %q is over %d bytes: %w", t.Name, MaxRunTargetNodeBytes, ErrInvalid)
		case names[t.Name]:
			return fmt.Errorf("target %q appears twice: %w", t.Name, ErrInvalid)
		case ids[t.NodeID]:
			return fmt.Errorf("node %q appears twice among the targets: %w", t.NodeID, ErrInvalid)
		}
		names[t.Name], ids[t.NodeID] = true, true
		if err := checkJSON("target node", t.Node); err != nil {
			return err
		}
		if !bytes.HasPrefix(bytes.TrimSpace(t.Node), []byte("{")) {
			return fmt.Errorf("the node of target %q is not a JSON object: %w", t.Name, ErrInvalid)
		}
	}
	return nil
}
