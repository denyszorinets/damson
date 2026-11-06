package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/pkg/pluginsdk"
	"github.com/denyszorinets/damson/plugins/all"
)

type nopInput struct{}

type fake struct{ m pluginsdk.Manifest }

func (f fake) Manifest() pluginsdk.Manifest                  { return f.m }
func (fake) Validate(context.Context, json.RawMessage) error { return nil }
func (fake) Execute(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
	return pluginsdk.Result{}, nil
}

func fakeManifest(name, version string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: name, Version: version, Title: name,
		InputSchema: pluginsdk.MustSchemaFor[nopInput](), OutputSchema: pluginsdk.MustSchemaFor[nopInput](),
		TargetMode: pluginsdk.TargetNone,
	}
}

func mod(path string, ps ...pluginsdk.Plugin) Module {
	return Module{Path: path, Plugins: func() []pluginsdk.Plugin { return ps }}
}

func TestLinkPlugins(t *testing.T) {
	builtin := all.Plugins()
	ssh := builtin[0].Manifest().Name
	bad := fakeManifest("Bad Name", "1.0.0")
	tests := []struct {
		name    string
		modules []Module
		want    string // error substring; empty means success
	}{
		{"none", nil, ""},
		{"new plugin", []Module{mod("ex.com/a", fake{fakeManifest("hello", "1.0.0")})}, ""},
		{"two versions", []Module{mod("ex.com/a", fake{fakeManifest("hello", "1.0.0")}, fake{fakeManifest("hello", "2.0.0")})}, ""},
		{"shadow builtin same version", []Module{mod("ex.com/a", fake{fakeManifest(ssh, builtin[0].Manifest().Version)})}, "shadow a built-in"},
		{"shadow builtin other version", []Module{mod("ex.com/a", fake{fakeManifest(ssh, "99.0.0")})}, "shadow a built-in"},
		{"same name two modules", []Module{mod("ex.com/a", fake{fakeManifest("hello", "1.0.0")}), mod("ex.com/b", fake{fakeManifest("hello", "2.0.0")})}, "both ex.com/a and ex.com/b"},
		{"duplicate version", []Module{mod("ex.com/a", fake{fakeManifest("hello", "1.0.0")}, fake{fakeManifest("hello", "1.0.0")})}, "provided twice"},
		{"invalid manifest", []Module{mod("ex.com/a", fake{bad})}, "invalid manifest"},
		{"panic", []Module{{Path: "ex.com/a", Plugins: func() []pluginsdk.Plugin { panic("boom") }}}, "panicked"},
		{"nil plugin", []Module{mod("ex.com/a", nil)}, "nil plugin"},
		{"no function", []Module{{Path: "ex.com/a"}}, "no Plugins function"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := linkPlugins(builtin, tc.modules)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(got) < len(builtin) {
					t.Fatalf("got %d plugins", len(got))
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLinkPluginsOrder(t *testing.T) {
	got, err := linkPlugins(all.Plugins(), []Module{mod("ex.com/a", fake{fakeManifest("zeta", "1.0.0")}, fake{fakeManifest("alpha", "1.0.0")})})
	if err != nil {
		t.Fatal(err)
	}
	n := len(got)
	if got[n-2].Manifest().Name != "zeta" || got[n-1].Manifest().Name != "alpha" {
		t.Fatal("extras are not appended in module order")
	}
}

func TestRunRejectsShadowingModuleForEveryCommand(t *testing.T) {
	ssh := all.Plugins()[0].Manifest().Name
	opts := Options{Modules: []Module{mod("ex.com/a", fake{fakeManifest(ssh, "99.0.0")})}}
	for _, cmd := range [][]string{{"version"}, {"plugin", "validate"}, {"agent"}} {
		var out, errb bytes.Buffer
		if code := Run(context.Background(), cmd, &out, &errb, nil, opts); code != exitError || !strings.Contains(errb.String(), "ex.com/a") {
			t.Errorf("%v: code %d, stderr %q", cmd, code, errb.String())
		}
	}
}

func TestVersionListsModulesAndPluginRunSeesExtras(t *testing.T) {
	opts := Options{Modules: []Module{mod("ex.com/a", fake{fakeManifest("hello", "1.0.0")})}}
	var out, errb bytes.Buffer
	if code := Run(context.Background(), []string{"version"}, &out, &errb, nil, opts); code != exitOK || !strings.Contains(out.String(), "with ex.com/a") {
		t.Fatalf("version: %d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := Run(context.Background(), []string{"plugin", "validate", "hello"}, &out, &errb, nil, opts); code != exitOK || !strings.Contains(out.String(), "hello@1.0.0") {
		t.Fatalf("plugin validate: %d %q %q", code, out.String(), errb.String())
	}
}
