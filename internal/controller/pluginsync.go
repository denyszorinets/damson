package controller

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
)

// liveWindow is how long an agent may stay unseen before its plugins stop
// counting: an agent of a crashed controller is no longer touched, so it ages
// out without any cleanup duty. last_seen is the heartbeat's arrival time and
// reaches the database up to one flush later, so the window covers two rounds
// of both intervals (it also absorbs some clock skew between controllers).
func (c Config) liveWindow() time.Duration { return 2 * (c.HeartbeatInterval + c.FlushInterval) }

// manifestKey identifies a stored manifest.
type manifestKey struct{ name, version, checksum string }

// Orphan manifests (no agent references them) are swept at most once per
// sweepEvery, and only when older than orphanAge: the age guard keeps the
// sweep from racing a hello that has just inserted a manifest.
const (
	sweepEvery = time.Hour
	orphanAge  = time.Hour
)

// pluginSyncer merges the plugins of agents connected to other controllers
// into the registry. It is used by one goroutine at a time.
type pluginSyncer struct {
	known     map[manifestKey]*pluginreg.Entry // nil value: stored but unusable
	lastSweep time.Time
}

// pluginSyncLoop refreshes the remote view once at start, so a controller
// that starts while its agents are live on other controllers knows their
// custom plugins immediately, then on every tick.
func (g *Gateway) pluginSyncLoop(ctx context.Context, t Ticker) {
	defer t.Stop()
	sy := &pluginSyncer{known: map[manifestKey]*pluginreg.Entry{}}
	g.syncPlugins(ctx, sy)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			g.syncPlugins(ctx, sy)
		}
	}
}

// syncPlugins installs the plugins of live agents owned by other controllers.
// On a store failure the previous remote view stays.
func (g *Gateway) syncPlugins(ctx context.Context, sy *pluginSyncer) {
	if g.cfg.Plugins == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, g.cfg.StoreTimeout)
	defer cancel()
	seenAfter := g.cfg.Clock.now().Add(-g.cfg.liveWindow())
	providers, err := g.cfg.Store.ListLivePluginProviders(sctx, seenAfter)
	if err != nil {
		if ctx.Err() == nil {
			g.log.Error("reading plugin providers of other controllers", "error", err)
		}
		return
	}
	var remote []store.LivePluginProvider
	missing := false
	for _, p := range providers {
		if p.ControllerID == g.cfg.ControllerID {
			continue
		}
		remote = append(remote, p)
		if _, ok := sy.known[manifestKey{p.Name, p.Version, p.Checksum}]; !ok {
			missing = true
		}
	}
	if missing {
		if err := sy.load(sctx, g.cfg.Store, g.cfg.Plugins, g.log); err != nil {
			if ctx.Err() == nil {
				g.log.Error("reading plugin manifests", "error", err)
			}
			return
		}
	}
	g.cfg.Plugins.SetRemote(sy.agents(remote))
	g.sweepManifests(sctx, sy)
}

// sweepManifests deletes unreferenced manifests; any controller may run it.
func (g *Gateway) sweepManifests(ctx context.Context, sy *pluginSyncer) {
	now := g.cfg.Clock.now()
	if now.Sub(sy.lastSweep) < sweepEvery {
		return
	}
	sy.lastSweep = now
	n, err := g.cfg.Store.DeleteOrphanPluginManifests(ctx, now.Add(-orphanAge))
	if err != nil {
		if ctx.Err() == nil {
			g.log.Error("sweeping orphan plugin manifests", "error", err)
		}
		return
	}
	if n > 0 {
		g.log.Info("swept orphan plugin manifests", "count", n)
	}
}

// load reads the stored manifests and validates each new checksum once. The
// known set is rebuilt from the table, so rows deleted by the sweep drop out.
func (sy *pluginSyncer) load(ctx context.Context, st store.PluginStore, sink PluginSink, log interface {
	Warn(string, ...any)
}) error {
	rows, err := st.ListPluginManifests(ctx)
	if err != nil {
		return err
	}
	next := make(map[manifestKey]*pluginreg.Entry, len(rows))
	for _, r := range rows {
		k := manifestKey{r.Name, r.Version, r.Checksum}
		if e, ok := sy.known[k]; ok {
			next[k] = e
			continue
		}
		e := pluginreg.Entry{Checksum: r.Checksum}
		err := json.Unmarshal(r.Manifest, &e.Manifest)
		if err == nil {
			err = sink.ValidateEntry(e)
		}
		if err != nil {
			log.Warn("stored plugin manifest ignored", "plugin", r.Name, "version", r.Version, "checksum", r.Checksum, "error", err.Error())
			next[k] = nil
			continue
		}
		next[k] = &e
	}
	sy.known = next
	return nil
}

// agents groups providers by agent. Capabilities are the agent row's keys
// that contain a colon: plugin names never do (hello builds the map from
// plugin names plus kind:value capabilities).
func (sy *pluginSyncer) agents(ps []store.LivePluginProvider) []pluginreg.RemoteAgent {
	var out []pluginreg.RemoteAgent
	idx := map[string]int{}
	for _, p := range ps {
		e := sy.known[manifestKey{p.Name, p.Version, p.Checksum}]
		if e == nil {
			continue
		}
		i, ok := idx[p.AgentID]
		if !ok {
			i = len(out)
			idx[p.AgentID] = i
			ra := pluginreg.RemoteAgent{ID: p.AgentID, Pool: p.Pool}
			for c := range p.Capabilities {
				if strings.Contains(c, ":") {
					ra.Caps = append(ra.Caps, c)
				}
			}
			out = append(out, ra)
		}
		out[i].Entries = append(out[i].Entries, *e)
	}
	return out
}
