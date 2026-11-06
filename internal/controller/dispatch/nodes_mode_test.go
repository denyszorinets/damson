package dispatch_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/controller/node"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// twoNodePlugin is a TargetNodes plugin: it logs the nodes it received and
// whether each node credential resolved.
type twoNodePlugin struct{}

func (twoNodePlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "twonodes", Version: "1.0.0", Title: "twonodes", TargetMode: pluginsdk.TargetNodes,
		NodePaths:   []string{"/source/node/name", "/destination/node/name"},
		InputSchema: pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (twoNodePlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (twoNodePlugin) Execute(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	var names []string
	for name, n := range req.Nodes {
		if _, err := req.Secrets.ResolveFields(n.Credential); err != nil {
			return pluginsdk.Result{Error: &pluginsdk.StepError{Message: "credential missing for " + name}}, nil
		}
		names = append(names, name)
	}
	sort.Strings(names)
	emit.Log(pluginsdk.Stdout, "nodes "+strings.Join(names, ","))
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

// pairSources knows nodes n1 and n2; n3 does not exist.
type pairSources struct {
	fakeSources
	n2Cred string // credential of n2; default secret://dev/cred
}

func (p *pairSources) Node(ctx context.Context, run, name string) (pluginsdk.NodeInfo, error) {
	if name == "n2" {
		cred := p.n2Cred
		if cred == "" {
			cred = "secret://dev/cred"
		}
		return pluginsdk.NodeInfo{Name: "n2", Address: "10.0.0.8", Credential: cred, HostKeyPolicy: "strict"}, nil
	}
	return p.fakeSources.Node(ctx, run, name)
}

// A TargetNodes step receives every node it names (source and destination),
// each with its credential, and nothing for an endpoint without a node.
func TestNodesModeDeliversEveryNamedNode(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		log   string
		want  store.RunStatus
	}{
		"both nodes": {`{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n2"}}}`, "nodes n1,n2", store.RunSucceeded},
		"same node":  {`{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n1"}}}`, "nodes n1", store.RunSucceeded},
		"one is s3":  {`{"source":{"s3":{"bucket":"b"}},"destination":{"node":{"name":"n2"}}}`, "nodes n2", store.RunSucceeded},
		"no node":    {`{"source":{"s3":{"bucket":"b"}},"destination":{"s3":{"bucket":"c"}}}`, "nodes ", store.RunSucceeded},
		"unknown":    {`{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n3"}}}`, "", store.RunFailed},
		"not a name": {`{"source":{"node":{"name":7}}}`, "", store.RunFailed},
		"empty name": {`{"destination":{"node":{"name":""}}}`, "", store.RunFailed},
	} {
		t.Run(name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				c := startClusterPlugins(t, st, nodeSpec, []pluginsdk.Plugin{twoNodePlugin{}}, func(cfg *dispatch.Config) {
					src := &pairSources{}
					cfg.Nodes, cfg.Secrets = src, src
				})
				tree := c.waitRun(c.triggerInput("twonodes", tc.input), tc.want)
				ex := tree.Steps[0].Executions[0]
				if tc.want == store.RunSucceeded {
					waitLog(t, st, ex.ID, tc.log)
				} else if ex.State != store.ExecFailed {
					t.Fatalf("execution = %s %q; want failed", ex.State, ex.Error)
				}
			})
		})
	}
}

// With project scoping on, the second node's credential is authorized like
// the first: the project's own and _inventory pass, another project's is
// denied as an unknown secret.
func TestNodesModeAuthorizesEveryCredential(t *testing.T) {
	kv := map[string]string{"value": secretMarker, "username": "u", "private_key": "k"}
	for _, tc := range []struct {
		name, cred string
		ok         bool
	}{
		{"own project", "secret://default/cred", true},
		{"inventory", "secret://_inventory/cred", true},
		{"another project", "secret://other/cred", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				b := newSecretBackend(t, st)
				for _, s := range [][2]string{{"default", "cred"}, {"other", "cred"}, {"_inventory", "cred"}} {
					putIn(t, b, s[0], s[1], kv)
				}
				log := slog.New(slog.DiscardHandler)
				c := startClusterPlugins(t, st, nodeSpec, []pluginsdk.Plugin{twoNodePlugin{}}, func(cfg *dispatch.Config) {
					cfg.Nodes = &pairSources{fakeSources: fakeSources{credRef: "secret://default/cred"}, n2Cred: tc.cred}
					cfg.Secrets = node.NewSecretSource(b, log)
					cfg.Project = func(context.Context, string) (string, error) { return "default", nil }
				})
				want := store.RunSucceeded
				if !tc.ok {
					want = store.RunFailed
				}
				tree := c.waitRun(c.triggerInput("twonodes", `{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n2"}}}`), want)
				if ex := tree.Steps[0].Executions[0]; !tc.ok && (!strings.Contains(ex.Error, "unknown secret") || strings.Contains(ex.Error, secretMarker)) {
					t.Fatalf("execution = %s %q", ex.State, ex.Error)
				}
			})
		})
	}
}
