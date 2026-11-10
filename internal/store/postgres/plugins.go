package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/postgres/gen"
)

var _ store.PluginStore = (*Store)(nil)

// replaceAgentPlugins stores the hello's manifests and replaces the agent's
// plugin set. It runs inside the hello transaction.
func replaceAgentPlugins(ctx context.Context, q *gen.Queries, h store.AgentHello, now time.Time) error {
	if err := q.DeleteAgentPlugins(ctx, h.ID); err != nil {
		return fmt.Errorf("clearing plugins of agent %q: %w", h.ID, err)
	}
	for _, m := range h.Plugins {
		err := q.InsertPluginManifest(ctx, gen.InsertPluginManifestParams{
			Name: m.Name, Version: m.Version, Checksum: m.Checksum, Manifest: m.Manifest, CreatedAt: ts(now),
		})
		if err != nil {
			return fmt.Errorf("storing manifest %s@%s: %w", m.Name, m.Version, err)
		}
		err = q.InsertAgentPlugin(ctx, gen.InsertAgentPluginParams{AgentID: h.ID, Name: m.Name, Version: m.Version, Checksum: m.Checksum})
		if err != nil {
			return fmt.Errorf("linking plugin %s@%s to agent %q: %w", m.Name, m.Version, h.ID, err)
		}
	}
	return nil
}

// ListLivePluginProviders implements store.PluginStore.
func (s *Store) ListLivePluginProviders(ctx context.Context, seenAfter time.Time) ([]store.LivePluginProvider, error) {
	rows, err := s.q.ListLivePluginProviders(ctx, ts(seenAfter))
	if err != nil {
		return nil, fmt.Errorf("listing live plugin providers: %w", err)
	}
	out := make([]store.LivePluginProvider, len(rows))
	for i, r := range rows {
		p := store.LivePluginProvider{AgentID: r.AgentID, Pool: r.Pool, ControllerID: r.ControllerID.String, Name: r.Name, Version: r.Version, Checksum: r.Checksum}
		if err := store.DecodeJSON(r.Capabilities, &p.Capabilities); err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

// ListPluginManifests implements store.PluginStore.
func (s *Store) ListPluginManifests(ctx context.Context) ([]store.PluginManifest, error) {
	rows, err := s.q.ListPluginManifests(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing plugin manifests: %w", err)
	}
	out := make([]store.PluginManifest, len(rows))
	for i, r := range rows {
		out[i] = store.PluginManifest{Name: r.Name, Version: r.Version, Checksum: r.Checksum, Manifest: r.Manifest}
	}
	return out, nil
}

// DeleteOrphanPluginManifests implements store.PluginStore.
func (s *Store) DeleteOrphanPluginManifests(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := s.q.DeleteOrphanPluginManifests(ctx, ts(olderThan))
	if err != nil {
		return 0, fmt.Errorf("deleting orphan plugin manifests: %w", err)
	}
	return int(n), nil
}
