package api_test

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

type apiIn struct {
	Host string `json:"host"`
}

type apiOut struct {
	OK bool `json:"ok"`
}

func apiManifest(name, version string, requires ...string) pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: name, Version: version, Title: name, Description: "d",
		InputSchema: pluginsdk.MustSchemaFor[apiIn](), OutputSchema: pluginsdk.MustSchemaFor[apiOut](),
		Requires: requires, TargetMode: pluginsdk.TargetNone,
	}
}

func pluginHandler(t *testing.T, reg *pluginreg.Registry) http.Handler {
	t.Helper()
	cfg := api.Config{Addr: "127.0.0.1:0", UI: ui(), Logger: slog.New(slog.DiscardHandler)}
	if reg != nil {
		cfg.Plugins = reg
	}
	return api.NewServer(cfg).Handler()
}

type pluginView struct {
	Name     string `json:"name"`
	Versions []struct {
		Version      string         `json:"version"`
		Builtin      bool           `json:"builtin"`
		Requires     []string       `json:"requires"`
		Pools        []string       `json:"pools"`
		InputSchema  map[string]any `json:"input_schema"`
		OutputSchema map[string]any `json:"output_schema"`
		Agents       []struct {
			AgentID string `json:"agent_id"`
			Pool    string `json:"pool"`
		} `json:"agents"`
		Conflicts []struct {
			Checksum string `json:"checksum"`
		} `json:"conflicts"`
	} `json:"versions"`
}

func TestPluginsEndpoints(t *testing.T) {
	reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0", "bin:ssh")})
	if err != nil {
		t.Fatal(err)
	}
	other := apiManifest("ssh", "1.0.0", "bin:ssh")
	other.Description = "different"
	for _, a := range []pluginreg.AgentInfo{
		{ID: "a1", Pool: "alpha", Manifests: []pluginsdk.Manifest{apiManifest("ssh", "1.0.0", "bin:ssh"), apiManifest("helm", "2.0.0")}},
		{ID: "a2", Pool: "beta", Manifests: []pluginsdk.Manifest{other}},
	} {
		if err := reg.SetAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	h := pluginHandler(t, reg)

	var list struct {
		Items []pluginView `json:"items"`
	}
	getJSON(t, h, "/api/v1/plugins", http.StatusOK, &list)
	if len(list.Items) != 2 || list.Items[0].Name != "helm" || list.Items[1].Name != "ssh" {
		t.Fatalf("list = %+v", list)
	}

	var ssh pluginView
	getJSON(t, h, "/api/v1/plugins/ssh", http.StatusOK, &ssh)
	if len(ssh.Versions) != 1 {
		t.Fatalf("ssh = %+v", ssh)
	}
	v := ssh.Versions[0]
	if !v.Builtin || v.Version != "1.0.0" || len(v.Requires) != 1 || len(v.Agents) != 1 || v.Agents[0].AgentID != "a1" ||
		len(v.Pools) != 1 || v.Pools[0] != "alpha" || len(v.Conflicts) != 1 {
		t.Errorf("version = %+v", v)
	}
	if v.InputSchema["properties"] == nil || v.OutputSchema["properties"] == nil {
		t.Errorf("schemas missing: %v / %v", v.InputSchema, v.OutputSchema)
	}

	rec := do(h, http.MethodGet, "/api/v1/plugins/missing")
	if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), api.ProblemContentType) {
		t.Errorf("missing = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestPluginsEmptyAndUnavailable(t *testing.T) {
	reg, err := pluginreg.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []pluginView `json:"items"`
	}
	getJSON(t, pluginHandler(t, reg), "/api/v1/plugins", http.StatusOK, &list)
	if list.Items == nil || len(list.Items) != 0 {
		t.Errorf("empty list should be [], got %+v", list.Items)
	}
	if rec := do(pluginHandler(t, nil), http.MethodGet, "/api/v1/plugins"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no catalog = %d", rec.Code)
	}
}
