package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PluginManifest is one plugin manifest stored under name+version+checksum.
// The checksum is part of the storage contract: readers use it as stored and
// never recompute it.
type PluginManifest struct {
	Name     string
	Version  string
	Checksum string
	Manifest json.RawMessage
}

// LivePluginProvider is one plugin reported by a connected agent.
type LivePluginProvider struct {
	AgentID      string
	Pool         string // pool name
	ControllerID string
	Capabilities map[string]string // as in the agent row: plugin names and kind:value capabilities
	Name         string
	Version      string
	Checksum     string
}

// PluginStore reads the persisted plugin registry. It is written by
// UpsertAgentOnHello, in the hello transaction.
type PluginStore interface {
	// ListLivePluginProviders returns the plugins of agents that are owned by
	// a controller, not disconnected, and seen at or after seenAfter (agents
	// of a crashed controller age out), ordered by agent id, name, version.
	ListLivePluginProviders(ctx context.Context, seenAfter time.Time) ([]LivePluginProvider, error)
	// ListPluginManifests returns every stored manifest. The table holds
	// distinct versions only, so it stays small.
	ListPluginManifests(ctx context.Context) ([]PluginManifest, error)
	// DeleteOrphanPluginManifests removes manifests created before olderThan
	// that no agent references, and returns how many. The age guard, with the
	// refresh of created_at on every link, keeps it from racing a concurrent
	// hello.
	DeleteOrphanPluginManifests(ctx context.Context, olderThan time.Time) (int, error)
}

// ValidatePluginManifest rejects a manifest the store cannot persist.
func ValidatePluginManifest(m PluginManifest) error {
	if m.Name == "" || m.Version == "" || m.Checksum == "" || len(m.Manifest) == 0 {
		return fmt.Errorf("plugin manifest needs name, version, checksum and manifest: %w", ErrInvalid)
	}
	return checkJSON("manifest", m.Manifest)
}
