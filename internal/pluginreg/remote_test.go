package pluginreg_test

import (
	"testing"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

func entry(t *testing.T, r *pluginreg.Registry, m pluginsdk.Manifest) pluginreg.Entry {
	t.Helper()
	sum, err := m.Checksum()
	if err != nil {
		t.Fatal(err)
	}
	e := pluginreg.Entry{Manifest: m, Checksum: sum}
	if err := r.ValidateEntry(e); err != nil {
		t.Fatal(err)
	}
	return e
}

func remote(id, pool string, caps []string, es ...pluginreg.Entry) pluginreg.RemoteAgent {
	return pluginreg.RemoteAgent{ID: id, Pool: pool, Caps: caps, Entries: es}
}

func TestRemoteAgentsAreResolvedAndListed(t *testing.T) {
	r := newReg(t)
	if _, err := r.Lookup("hello"); err == nil {
		t.Fatal("unknown plugin found before SetRemote")
	}
	r.SetRemote([]pluginreg.RemoteAgent{remote("r1", "p", []string{"bin:git"}, entry(t, r, man("hello", "1.0.0", "bin:git")))})
	res, err := r.Resolve("hello@1", pluginreg.Filter{Pool: "p"})
	if err != nil || len(res.Agents) != 1 || res.Agents[0].AgentID != "r1" {
		t.Fatalf("Resolve = %+v, %v", res, err)
	}
	if got := r.List(); len(got) != 1 || got[0].Name != "hello" {
		t.Fatalf("List = %+v", got)
	}
	if len(r.Agents()) != 0 {
		t.Fatalf("Agents() = %v; remote agents are not local", r.Agents())
	}
	// A requirement the remote agent lacks still excludes it.
	r.SetRemote([]pluginreg.RemoteAgent{remote("r1", "p", nil, entry(t, r, man("hello", "1.0.0", "bin:git")))})
	if res, err := r.Resolve("hello@1", pluginreg.Filter{Pool: "p"}); err == nil {
		t.Fatalf("resolved to an agent without the capability: %+v", res)
	}
	// SetRemote replaces the previous view and invalidates the cache.
	r.SetRemote(nil)
	if got := r.List(); len(got) != 0 {
		t.Fatalf("List after clearing = %+v", got)
	}
}

func TestLocalAgentWinsIdCollision(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", nil, man("hello", "1.0.0"))
	r.SetRemote([]pluginreg.RemoteAgent{remote("a1", "p", nil, entry(t, r, man("hello", "1.1.0")))})
	p, _ := r.Get("hello")
	if len(p.Versions) != 1 || p.Versions[0].Manifest.Version != "1.0.0" || len(p.Versions[0].Agents) != 1 {
		t.Fatalf("hello = %+v; the local record must win and count once", p)
	}
}

func TestConflictMajorityCountsRemoteAgents(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "l1", "p", nil, man("hello", "1.0.0"))
	alt := entry(t, r, altMan("hello", "1.0.0"))
	r.SetRemote([]pluginreg.RemoteAgent{remote("r1", "p", nil, alt), remote("r2", "p", nil, alt)})
	p, _ := r.Get("hello")
	v := p.Versions[0]
	if v.Checksum != alt.Checksum || len(v.Agents) != 2 || len(v.Conflicts) != 1 || ids(v.Conflicts[0].Agents)[0] != "l1" {
		t.Fatalf("version = %+v; the two remote agents should win and the local one conflict", v)
	}
}

func TestRemoteCannotShadowBuiltin(t *testing.T) {
	r := newReg(t, man("ssh", "1.0.0"))
	r.SetRemote([]pluginreg.RemoteAgent{remote("r1", "p", nil, entry(t, r, man("ssh", "2.0.0")))})
	m, err := r.Lookup("ssh")
	if err != nil || m.Version != "1.0.0" {
		t.Fatalf("unpinned ssh = %+v, %v; want the built-in 1.0.0", m, err)
	}
}

func TestPreparedEntries(t *testing.T) {
	r := newReg(t)
	p, err := r.Prepare(pluginreg.AgentInfo{ID: "a", Pool: "p", Manifests: []pluginsdk.Manifest{man("b", "1.0.0"), man("a", "2.0.0"), man("a", "1.0.0")}})
	if err != nil {
		t.Fatal(err)
	}
	es := p.Entries()
	if len(es) != 3 || es[0].Manifest.Name != "a" || es[0].Manifest.Version != "1.0.0" || es[2].Manifest.Name != "b" || es[0].Checksum == "" {
		t.Fatalf("Entries = %+v", es)
	}
}
