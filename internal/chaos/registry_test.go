//go:build chaos

package chaos

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// namedPlugin is a trivial plugin under a chosen name; it counts its runs.
type namedPlugin struct {
	name string
	runs *atomic.Int32
}

func newNamedPlugin(name string) namedPlugin { return namedPlugin{name: name, runs: new(atomic.Int32)} }

func (p namedPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: p.name, Version: "1.0.0", Title: p.name, TargetMode: pluginsdk.TargetNone,
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (p namedPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (p namedPlugin) Execute(context.Context, pluginsdk.ExecRequest, pluginsdk.Emitter) (pluginsdk.Result, error) {
	p.runs.Add(1)
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

// registryView is GET /api/v1/plugins of controller idx reduced to what must
// agree across controllers: plugin version -> checksum and sorted providers.
func (c *Cluster) registryView(idx int) map[string]string {
	c.t.Helper()
	resp, err := http.Get(c.ctls[idx].srv.URL + "/api/v1/plugins")
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("GET /plugins on %s = %d", c.ctls[idx].id, resp.StatusCode)
	}
	var body struct{ Items []api.Plugin }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		c.t.Fatal(err)
	}
	view := map[string]string{}
	for _, p := range body.Items {
		for _, v := range p.Versions {
			var ids []string
			for _, a := range v.Agents {
				ids = append(ids, a.AgentID+"@"+a.Pool)
			}
			sort.Strings(ids)
			b, _ := json.Marshal([]any{v.Checksum, ids, v.Conflicts})
			view[p.Name+"@"+v.Version] = string(b)
		}
	}
	return view
}

// C (#63): plugins of agents on different controllers show up in both
// controllers' registry, and a run of a plugin that only the other
// controller's agent provides finishes there.
func TestPluginRegistryConsistentAcrossControllers(t *testing.T) {
	c := newCluster(t, 2)
	alpha, beta := newNamedPlugin("alpha"), newNamedPlugin("beta")
	// The job's second step runs "gate", which only ag1 provides.
	c.addAgent("ag1", 0, alpha, newGatePlugin())
	c.addAgent("ag2", 1, beta)

	eventually(t, "both controllers to list alpha, beta and gate with their agents", func() bool {
		a, b := c.registryView(0), c.registryView(1)
		return len(a) == 3 && reflect.DeepEqual(a, b)
	})
	view := c.registryView(0)
	for _, k := range []string{"alpha@1.0.0", "beta@1.0.0"} {
		if _, ok := view[k]; !ok {
			t.Fatalf("registry %v lacks %s", view, k)
		}
	}

	// Triggered through A, which has no agent for beta.
	runID := c.triggerPlugin(0, "beta")
	eventually(t, "run to finish", func() bool {
		tree, err := c.ctls[0].st.GetRunTree(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		return tree.Status.Terminal()
	})
	tree, _ := c.ctls[0].st.GetRunTree(context.Background(), runID)
	if tree.Status != store.RunSucceeded || beta.runs.Load() != 1 || alpha.runs.Load() != 0 {
		t.Fatalf("status %s, beta ran %d, alpha ran %d; want succeeded, 1, 0", tree.Status, beta.runs.Load(), alpha.runs.Load())
	}
}
