package controller

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/protocol"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type pIn struct {
	Host string `json:"host"`
}

type pOut struct {
	OK bool `json:"ok"`
}

func pManifest(version string, requires ...string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "demo", Version: version, Title: "Demo",
		InputSchema: pluginsdk.MustSchemaFor[pIn](), OutputSchema: pluginsdk.MustSchemaFor[pOut](),
		Requires: requires, TargetMode: pluginsdk.TargetNone,
	}
}

func helloWithManifests(seq uint64, ms ...pluginsdk.Manifest) protocol.HelloRequest {
	req := defaultHello(seq)
	for _, m := range ms {
		req.Manifests = append(req.Manifests, protocol.WireManifest{Manifest: m})
	}
	return req
}

func TestHelloFeedsPluginRegistry(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		reg, err := pluginreg.New(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := newRig(t, st, func(c *Config) { c.Plugins = reg })

		a := r.dial("agent-1")
		bad := pManifest("nope")
		if _, err := a.hello(helloWithManifests(1, pManifest("1.0.0", "bin:git"), bad)); err != nil {
			t.Fatal(err)
		}
		// An invalid manifest is skipped (logged), not fatal for the hello.
		res, err := reg.Resolve("demo@1", pluginreg.Filter{Pool: "default"})
		if err != nil || len(res.Agents) != 1 || res.Agents[0].AgentID != "agent-1" {
			t.Fatalf("resolve = %+v, %v", res, err)
		}

		// A newer session for the same agent replaces the manifests.
		b := r.dial("agent-1")
		if _, err := b.hello(helloWithManifests(2, pManifest("1.1.0", "bin:git"))); err != nil {
			t.Fatal(err)
		}
		eventually(t, "manifests replaced", func() bool {
			res, err := reg.Resolve("demo", pluginreg.Filter{})
			return err == nil && res.Manifest.Version == "1.1.0"
		})
		a.close() // the superseded socket must not remove the new session's entry
		b.heartbeat(protocol.Heartbeat{})
		if _, err := reg.Resolve("demo", pluginreg.Filter{}); err != nil {
			t.Fatalf("superseded disconnect removed the live entry: %v", err)
		}

		b.close()
		eventually(t, "plugins removed on disconnect", func() bool { return len(reg.Agents()) == 0 })
		if _, err := reg.Resolve("demo", pluginreg.Filter{}); err == nil {
			t.Error("plugin still resolvable after disconnect")
		}
	})
}

// Concurrent hellos with different session_seq: whichever order they finish
// in, the plugin registry ends with the manifests of the highest seq only.
func TestConcurrentHellosKeepHighestSeqPlugins(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		reg, err := pluginreg.New(nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		r := newRig(t, st, func(c *Config) { c.Plugins = reg })
		const n = 6
		agents := make([]*agent, n)
		for i := range agents {
			agents[i] = r.dial("agent-1")
		}
		var wg sync.WaitGroup
		for i, a := range agents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = a.hello(helloWithManifests(uint64(i+1), pManifest(fmt.Sprintf("1.%d.0", i+1))))
			}()
		}
		wg.Wait()
		eventually(t, "highest session's manifests only", func() bool {
			p, ok := reg.Get("demo")
			s := r.gw.reg.get("agent-1")
			return ok && len(p.Versions) == 1 && p.Versions[0].Manifest.Version == fmt.Sprintf("1.%d.0", n) &&
				s != nil && s.seqValue() == n
		})
	})
}

func pluginNames(reg *pluginreg.Registry) []string {
	var out []string
	for _, p := range reg.List() {
		out = append(out, p.Name)
	}
	return out
}

type failingPlugins struct {
	store.Store
	fail *atomic.Bool
}

func (f failingPlugins) ListLivePluginProviders(ctx context.Context, after time.Time) ([]store.LivePluginProvider, error) {
	if f.fail.Load() {
		return nil, errors.New("database unavailable")
	}
	return f.Store.ListLivePluginProviders(ctx, after)
}

func newSyncer() *pluginSyncer { return &pluginSyncer{known: map[manifestKey]*pluginreg.Entry{}} }

// Plugins reported to one controller become known to the other through the
// store, and are dropped again on disconnect or when the agent stops being
// seen (a crashed controller). The sync is driven directly, with a clock that
// only moves where the test says so.
func TestPluginsSyncAcrossControllers(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		ctx := context.Background()
		reg1, _ := pluginreg.New(nil, nil)
		reg2, _ := pluginreg.New(nil, nil)
		r1 := newRig(t, st, func(c *Config) { c.Plugins = reg1 })
		r2 := newPeerRig(t, r1, "ctl-2", func(c *Config) { c.Plugins = reg2 })
		sy := newSyncer()
		sync2 := func() { r2.gw.syncPlugins(ctx, sy) }

		a := r1.dial("agent-1")
		if _, err := a.hello(helloWithManifests(1, pManifest("1.0.0", "bin:git"))); err != nil {
			t.Fatal(err)
		}
		sync2()
		res, err := reg2.Resolve("demo@1", pluginreg.Filter{Pool: "default"})
		if err != nil || len(res.Agents) != 1 || res.Agents[0].AgentID != "agent-1" || res.Agents[0].Pool != "default" {
			t.Fatalf("ctl-2 resolve = %+v, %v", res, err)
		}
		if _, err := reg2.Lookup("demo@1"); err != nil {
			t.Fatalf("ctl-2 lookup: %v", err)
		}

		// A store failure keeps the previous view.
		fail := &atomic.Bool{}
		fail.Store(true)
		r3 := newPeerRig(t, r1, "ctl-3", func(c *Config) { c.Store = failingPlugins{st, fail}; c.Plugins = reg2 })
		r3.gw.syncPlugins(ctx, newSyncer())
		if got := pluginNames(reg2); len(got) != 1 {
			t.Fatalf("view after a failed sync = %v; want it kept", got)
		}

		// A disconnect on ctl-1 removes it from ctl-2's view, with no clock
		// movement: the agent is still inside the live window.
		a.close()
		eventually(t, "ctl-2 drops demo after disconnect", func() bool {
			sync2()
			return len(pluginNames(reg2)) == 0
		})

		// An agent that is never touched again (controller crash) ages out.
		b := r1.dial("agent-2")
		if _, err := b.hello(helloWithManifests(1, pManifest("1.0.0"))); err != nil {
			t.Fatal(err)
		}
		sync2()
		if got := pluginNames(reg2); len(got) != 1 {
			t.Fatalf("ctl-2 view = %v; want demo", got)
		}
		r2.clock.advance(r2.gw.cfg.liveWindow() + time.Second)
		sync2()
		if got := pluginNames(reg2); len(got) != 0 {
			t.Fatalf("ctl-2 view = %v; the unseen agent should have aged out", got)
		}
		if got := pluginNames(reg1); len(got) != 1 {
			t.Fatalf("ctl-1 lost its own agent's plugin: %v", got)
		}
	})
}
