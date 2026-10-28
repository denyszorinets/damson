package pluginreg_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/invopop/jsonschema"

	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type testIn struct {
	Host  string `json:"host" jsonschema:"pattern=^[a-z.]+$"`
	Port  int    `json:"port,omitempty" jsonschema:"minimum=1,maximum=65535"`
	Token string `json:"token,omitempty"`
	Mode  string `json:"mode,omitempty" jsonschema:"enum=fast,enum=slow"`
}

type altIn struct {
	Host string `json:"host"`
}

type testOut struct {
	OK bool `json:"ok"`
}

func man(name, version string, requires ...string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: name, Version: version, Title: name,
		InputSchema: pluginsdk.MustSchemaFor[testIn](), OutputSchema: pluginsdk.MustSchemaFor[testOut](),
		Requires: requires, TargetMode: pluginsdk.TargetNone,
	}
}

func altMan(name, version string) pluginsdk.Manifest {
	m := man(name, version)
	m.InputSchema = pluginsdk.MustSchemaFor[altIn]()
	return m
}

func newReg(t *testing.T, builtins ...pluginsdk.Manifest) *pluginreg.Registry {
	t.Helper()
	r, err := pluginreg.New(builtins, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setAgent(t *testing.T, r *pluginreg.Registry, id, pool string, caps []string, ms ...pluginsdk.Manifest) {
	t.Helper()
	if err := r.SetAgent(pluginreg.AgentInfo{ID: id, Pool: pool, Capabilities: caps, Manifests: ms}); err != nil {
		t.Fatal(err)
	}
}

func ids(ps []pluginreg.Provider) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.AgentID)
	}
	return out
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		in      string
		want    pluginreg.Ref
		wantErr bool
	}{
		{"ssh", pluginreg.Ref{Name: "ssh"}, false},
		{"helm@1", pluginreg.Ref{Name: "helm", Major: 1, Pinned: true}, false},
		{"helm@0", pluginreg.Ref{Name: "helm", Major: 0, Pinned: true}, false},
		{"", pluginreg.Ref{}, true},
		{"@1", pluginreg.Ref{}, true},
		{"helm@", pluginreg.Ref{}, true},
		{"helm@1.2", pluginreg.Ref{}, true},
		{"helm@01", pluginreg.Ref{}, true},
		{"helm@x", pluginreg.Ref{}, true},
		{"he lm", pluginreg.Ref{}, true},
	}
	for _, tt := range tests {
		got, err := pluginreg.ParseRef(tt.in)
		if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, pluginreg.ErrInvalidRef)) {
			t.Fatalf("ParseRef(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseRef(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestResolveVersions(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", nil, man("helm", "1.2.0"), man("helm", "2.0.0-rc.1"))
	setAgent(t, r, "a2", "p", nil, man("helm", "1.10.0"), man("helm", "2.0.0"))
	setAgent(t, r, "a3", "q", nil, man("helm", "1.9.9"))
	tests := []struct {
		name, ref, pool string
		wantVer         string
		wantAgents      []string
		wantErr         error
	}{
		{"unpinned highest", "helm", "", "2.0.0", []string{"a2"}, nil},
		{"pin major 1 numeric compare", "helm@1", "", "1.10.0", []string{"a2"}, nil},
		{"pin 2 skips prerelease", "helm@2", "", "2.0.0", []string{"a2"}, nil},
		{"pool filter falls back", "helm", "q", "1.9.9", []string{"a3"}, nil},
		{"pin without version", "helm@3", "", "", nil, pluginreg.ErrNoVersion},
		{"unknown", "nope", "", "", nil, pluginreg.ErrUnknownPlugin},
		{"unknown pool", "helm", "zzz", "", nil, pluginreg.ErrNoCapableAgent},
		{"bad ref", "helm@x", "", "", nil, pluginreg.ErrInvalidRef},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := r.Resolve(tt.ref, pluginreg.Filter{Pool: tt.pool})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if res.Manifest.Version != tt.wantVer || !reflect.DeepEqual(ids(res.Agents), tt.wantAgents) {
				t.Errorf("got %s %v, want %s %v", res.Manifest.Version, ids(res.Agents), tt.wantVer, tt.wantAgents)
			}
		})
	}
}

func TestResolveCapabilities(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", []string{"bin:git"}, man("git", "1.0.0", "bin:git", "bin:ssh"))
	setAgent(t, r, "a2", "p", []string{"bin:git", "bin:ssh"}, man("git", "1.0.0", "bin:git", "bin:ssh"))
	setAgent(t, r, "a3", "p", []string{"bin:git"}, man("git", "0.9.0", "bin:git"))
	res, err := r.Resolve("git", pluginreg.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Version != "1.0.0" || !reflect.DeepEqual(ids(res.Agents), []string{"a2"}) {
		t.Errorf("got %s %v", res.Manifest.Version, ids(res.Agents))
	}
	// Only the agent lacking nothing for 0.9.0 remains once a2 leaves.
	r.RemoveAgent("a2")
	res, err = r.Resolve("git", pluginreg.Filter{})
	if err != nil || res.Manifest.Version != "0.9.0" || !reflect.DeepEqual(ids(res.Agents), []string{"a3"}) {
		t.Errorf("after removal: %+v %v", res, err)
	}
}

func TestBuiltinKnownWithoutAgents(t *testing.T) {
	r := newReg(t, man("ssh", "1.0.0"))
	if _, err := r.Resolve("ssh", pluginreg.Filter{}); !errors.Is(err, pluginreg.ErrNoCapableAgent) {
		t.Fatalf("Resolve err = %v", err)
	}
	m, err := r.Lookup("ssh@1")
	if err != nil || m.Version != "1.0.0" {
		t.Fatalf("Lookup = %v %v", m.Version, err)
	}
	p, ok := r.Get("ssh")
	if !ok || !p.Versions[0].Builtin || len(p.Versions[0].Agents) != 0 {
		t.Errorf("Get = %+v", p)
	}
}

func TestDedupeAndDisconnect(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", nil, man("ssh", "1.0.0"))
	setAgent(t, r, "a2", "q", nil, man("ssh", "1.0.0"))
	p, _ := r.Get("ssh")
	if len(p.Versions) != 1 || len(p.Versions[0].Conflicts) != 0 {
		t.Fatalf("expected one deduped version, got %+v", p.Versions)
	}
	if got := p.Versions[0].Pools(); !reflect.DeepEqual(got, []string{"p", "q"}) {
		t.Errorf("pools = %v", got)
	}
	r.RemoveAgent("a1")
	r.RemoveAgent("a1") // idempotent
	p, _ = r.Get("ssh")
	if got := ids(p.Versions[0].Agents); !reflect.DeepEqual(got, []string{"a2"}) {
		t.Errorf("agents = %v", got)
	}
	r.RemoveAgent("a2")
	if _, ok := r.Get("ssh"); ok {
		t.Error("plugin should vanish with its last provider")
	}
	if len(r.Agents()) != 0 {
		t.Errorf("agents = %v", r.Agents())
	}
}

func TestReplaceAgentManifests(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", nil, man("ssh", "1.0.0"))
	setAgent(t, r, "a1", "p", nil, man("ssh", "1.1.0")) // reconnect with an upgraded binary
	p, _ := r.Get("ssh")
	if len(p.Versions) != 1 || p.Versions[0].Manifest.Version != "1.1.0" {
		t.Errorf("versions = %+v", p.Versions)
	}
}

func TestConflictingManifests(t *testing.T) {
	r := newReg(t)
	setAgent(t, r, "a1", "p", nil, man("x", "1.0.0"))
	setAgent(t, r, "a2", "p", nil, man("x", "1.0.0"))
	setAgent(t, r, "a3", "p", nil, altMan("x", "1.0.0"))
	p, _ := r.Get("x")
	v := p.Versions[0]
	if got := ids(v.Agents); !reflect.DeepEqual(got, []string{"a1", "a2"}) {
		t.Errorf("majority agents = %v", got)
	}
	if len(v.Conflicts) != 1 || !reflect.DeepEqual(ids(v.Conflicts[0].Agents), []string{"a3"}) {
		t.Fatalf("conflicts = %+v", v.Conflicts)
	}
	res, err := r.Resolve("x", pluginreg.Filter{})
	if err != nil || !reflect.DeepEqual(ids(res.Agents), []string{"a1", "a2"}) {
		t.Errorf("resolve = %+v %v", res, err)
	}
}

func TestConflictBuiltinWinsAndTieIsDeterministic(t *testing.T) {
	r := newReg(t, man("x", "1.0.0"))
	setAgent(t, r, "a1", "p", nil, altMan("x", "1.0.0"))
	setAgent(t, r, "a2", "p", nil, altMan("x", "1.0.0"))
	if _, err := r.Resolve("x", pluginreg.Filter{}); !errors.Is(err, pluginreg.ErrNoCapableAgent) {
		t.Errorf("minority agents must be refused: %v", err)
	}
	p, _ := r.Get("x")
	if !p.Versions[0].Builtin || len(p.Versions[0].Conflicts) != 1 {
		t.Errorf("builtin should win: %+v", p.Versions[0])
	}

	// One agent each, no builtin: the same winner on every call.
	r2 := newReg(t)
	setAgent(t, r2, "a1", "p", nil, man("y", "1.0.0"))
	setAgent(t, r2, "a2", "p", nil, altMan("y", "1.0.0"))
	first, _ := r2.Resolve("y", pluginreg.Filter{})
	for range 20 {
		again, _ := r2.Resolve("y", pluginreg.Filter{})
		if again.Checksum != first.Checksum {
			t.Fatal("tie break is not deterministic")
		}
	}
}

func TestSetAgentSkipsBadManifests(t *testing.T) {
	r := newReg(t)
	bad := man("ok", "1.0.0")
	bad.Version = "nope"
	err := r.SetAgent(pluginreg.AgentInfo{ID: "a1", Pool: "p", Manifests: []pluginsdk.Manifest{bad, man("ok", "1.0.0"), man("ok", "1.0.0")}})
	if err == nil || !strings.Contains(err.Error(), "skipped") || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("err = %v", err)
	}
	if _, e := r.Resolve("ok", pluginreg.Filter{}); e != nil {
		t.Errorf("valid manifest should still install: %v", e)
	}
}

func TestNewRejectsBadBuiltins(t *testing.T) {
	bad := man("a", "1.0.0")
	bad.Title = ""
	if _, err := pluginreg.New([]pluginsdk.Manifest{bad}, nil); err == nil {
		t.Error("invalid builtin accepted")
	}
	if _, err := pluginreg.New([]pluginsdk.Manifest{man("a", "1.0.0"), man("a", "1.0.0")}, nil); err == nil {
		t.Error("duplicate builtin accepted")
	}
}

func TestValidateInput(t *testing.T) {
	r := newReg(t)
	m := man("t", "1.0.0")
	tests := []struct {
		name, input string
		wantPaths   []string
		leak        string // must not appear in any message
	}{
		{"ok", `{"host":"a.b","port":22}`, nil, ""},
		{"secret ref allowed", `{"host":"a.b","token":"secret://prod/token"}`, nil, ""},
		{"missing required", `{"port":22}`, []string{"$"}, ""},
		{"wrong type", `{"host":"a.b","port":"22"}`, []string{"port"}, ""},
		{"range", `{"host":"a.b","port":70000}`, []string{"port"}, ""},
		{"unknown field", `{"host":"a.b","bogus":1}`, []string{"$"}, ""},
		{"enum", `{"host":"a.b","mode":"warp"}`, []string{"mode"}, ""},
		{"pattern does not echo", `{"host":"SuperSecretValue1"}`, []string{"host"}, "SuperSecretValue1"},
		{"secret in wrong type does not echo", `{"host":"a.b","port":"secret-plaintext-9"}`, []string{"port"}, "secret-plaintext-9"},
		{"not json", `{`, []string{"$"}, ""},
		{"not object", `[]`, []string{"$"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, validate := range []func() error{
				func() error { return pluginreg.ValidateInput(m, json.RawMessage(tt.input)) },
				func() error { return r.ValidateInput(m, json.RawMessage(tt.input)) },
			} {
				err := validate()
				if tt.wantPaths == nil {
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					continue
				}
				var ve pluginreg.ValidationErrors
				if !errors.As(err, &ve) {
					t.Fatalf("err = %v, want ValidationErrors", err)
				}
				var paths []string
				for _, fe := range ve {
					paths = append(paths, fe.Path)
				}
				if !reflect.DeepEqual(paths, tt.wantPaths) {
					t.Errorf("paths = %v, want %v (%v)", paths, tt.wantPaths, err)
				}
				if tt.leak != "" && strings.Contains(err.Error(), tt.leak) {
					t.Errorf("error echoes the input value: %v", err)
				}
			}
		})
	}
}

func TestValidateInputDeniesRemoteRefs(t *testing.T) {
	m := man("t", "1.0.0")
	m.InputSchema = &jsonschema.Schema{Ref: "file:///etc/passwd"}
	if err := pluginreg.ValidateInput(m, json.RawMessage(`{}`)); err == nil {
		t.Fatal("a file: $ref must fail to compile")
	} else if errors.As(err, new(pluginreg.ValidationErrors)) {
		t.Fatalf("expected a compile error, got %v", err)
	}
}
