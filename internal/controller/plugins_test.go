package controller

import (
	"testing"

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
		reg, err := pluginreg.New(nil)
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
