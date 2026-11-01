package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
)

func pm(name, version, sum string) store.PluginManifest {
	return store.PluginManifest{Name: name, Version: version, Checksum: sum, Manifest: []byte(`{"name":"` + name + `","version":"` + version + `"}`)}
}

func helloWith(id, poolID, ctrl string, seq uint64, ps ...store.PluginManifest) store.AgentHello {
	h := hello(id, poolID, ctrl)
	h.SessionSeq, h.Plugins = seq, ps
	h.Capabilities = map[string]string{"bin:git": ""}
	return h
}

func providerSet(ps []store.LivePluginProvider) map[string]bool {
	out := map[string]bool{}
	for _, p := range ps {
		out[p.AgentID+"/"+p.Name+"@"+p.Version+"#"+p.Checksum] = true
	}
	return out
}

func testPluginManifests(t *testing.T, s store.Store) {
	ctx := context.Background()
	migrated(t, s)
	pool := mustPool(t, s, "plug")
	seenAfter := t0.Add(-time.Minute)

	// Two agents with the same manifest share one row.
	if err := s.UpsertAgentOnHello(ctx, helloWith("a1", pool.ID, "c1", 1, pm("hello", "1.0.0", "x"), pm("other", "2.0.0", "y")), t0); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentOnHello(ctx, helloWith("a2", pool.ID, "c2", 1, pm("hello", "1.0.0", "x")), t0); err != nil {
		t.Fatal(err)
	}
	if ms, err := s.ListPluginManifests(ctx); err != nil || len(ms) != 2 {
		t.Fatalf("ListPluginManifests = %+v, %v; want 2 rows", ms, err)
	} else {
		jsonEq(t, ms[0].Manifest, `{"name":"hello","version":"1.0.0"}`)
	}
	live, err := s.ListLivePluginProviders(ctx, seenAfter)
	if err != nil {
		t.Fatal(err)
	}
	if got := providerSet(live); len(live) != 3 || !got["a1/hello@1.0.0#x"] || !got["a1/other@2.0.0#y"] || !got["a2/hello@1.0.0#x"] {
		t.Fatalf("providers = %+v", live)
	}
	if live[0].Pool != "plug" || live[0].ControllerID != "c1" || live[0].Capabilities["bin:git"] != "" {
		t.Fatalf("provider row = %+v", live[0])
	}

	// A newer session replaces the set, including shrinking it.
	if err := s.UpsertAgentOnHello(ctx, helloWith("a1", pool.ID, "c1", 2, pm("hello", "1.1.0", "z")), t0); err != nil {
		t.Fatal(err)
	}
	live, _ = s.ListLivePluginProviders(ctx, seenAfter)
	if got := providerSet(live); len(live) != 2 || !got["a1/hello@1.1.0#z"] || !got["a2/hello@1.0.0#x"] {
		t.Fatalf("providers after replace = %+v", live)
	}

	// A stale session or a pool conflict writes nothing.
	other := mustPool(t, s, "plug-other")
	for name, h := range map[string]store.AgentHello{
		"stale":    helloWith("a1", pool.ID, "c9", 2, pm("evil", "1.0.0", "e")),
		"conflict": helloWith("a1", other.ID, "c9", 9, pm("evil", "1.0.0", "e")),
	} {
		if err := s.UpsertAgentOnHello(ctx, h, t0); !errors.Is(err, store.ErrStale) && !errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s hello: %v", name, err)
		}
	}
	live, _ = s.ListLivePluginProviders(ctx, seenAfter)
	if got := providerSet(live); len(live) != 2 || got["a1/evil@1.0.0#e"] {
		t.Fatalf("providers after refused hellos = %+v", live)
	}
	if ms, _ := s.ListPluginManifests(ctx); len(ms) != 3 { // hello 1.0.0, other 2.0.0, hello 1.1.0; evil never stored
		t.Fatalf("manifests = %+v; a refused hello must store nothing", ms)
	}

	// Invalid manifests are rejected.
	bad := helloWith("a3", pool.ID, "c1", 1, store.PluginManifest{Name: "n", Version: "1", Checksum: "c"})
	if err := s.UpsertAgentOnHello(ctx, bad, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("hello with empty manifest: %v; want ErrInvalid", err)
	}
	dup := helloWith("a3", pool.ID, "c1", 1, pm("n", "1.0.0", "c"), pm("n", "1.0.0", "d"))
	if err := s.UpsertAgentOnHello(ctx, dup, t0); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("hello repeating a plugin: %v; want ErrInvalid", err)
	}

	// Liveness: disconnected, unowned and long-unseen agents are excluded.
	if err := s.MarkAgentDisconnected(ctx, "a2", "c2", 1, t0); err != nil {
		t.Fatal(err)
	}
	live, _ = s.ListLivePluginProviders(ctx, seenAfter)
	if len(live) != 1 || live[0].AgentID != "a1" {
		t.Fatalf("providers after disconnect = %+v; want only a1", live)
	}
	if live, _ = s.ListLivePluginProviders(ctx, t0.Add(time.Hour)); len(live) != 0 {
		t.Fatalf("providers seen before the cutoff = %+v; want none", live)
	}

	// The orphan sweep keeps referenced rows and honors the age guard.
	if n, err := s.DeleteOrphanPluginManifests(ctx, t0.Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("sweep before the age guard = %d, %v; want 0", n, err)
	}
	n, err := s.DeleteOrphanPluginManifests(ctx, t0.Add(time.Hour))
	if err != nil || n != 1 { // a2 still links hello@1.0.0; a1 dropped other@2.0.0
		t.Fatalf("sweep = %d, %v; want the unreferenced other@2.0.0 only", n, err)
	}
}
