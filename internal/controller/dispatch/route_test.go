package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// poolSources knows nodes n1..n4 and the pool of each: n1 dc1, n2 dc2, n3 none.
type poolSources struct {
	fakeSources
	pools map[string]string
}

func (p *poolSources) Node(ctx context.Context, _, name string) (pluginsdk.NodeInfo, error) {
	if _, ok := p.pools[name]; !ok {
		return pluginsdk.NodeInfo{}, dispatch.ErrUnknownNode
	}
	return pluginsdk.NodeInfo{Name: name, Address: "10.0.0.7", Credential: "secret://dev/cred", HostKeyPolicy: "strict"}, nil
}

func (p *poolSources) NodePool(_ context.Context, _, name string) (string, error) {
	pool, ok := p.pools[name]
	if !ok {
		return "", dispatch.ErrUnknownNode
	}
	return pool, nil
}

func newPoolSources() *poolSources {
	return &poolSources{pools: map[string]string{"n1": "dc1", "n2": "dc2", "n3": "", "n4": "dc1"}}
}

// routeCluster has an agent in "default" (agent-1) and one in dc1 (agent-dc1).
func routeCluster(t *testing.T, st store.Store, src *poolSources) *cluster {
	t.Helper()
	c := startClusterPlugins(t, st, nodeSpec, []pluginsdk.Plugin{twoNodePlugin{}}, func(cfg *dispatch.Config) {
		cfg.Nodes, cfg.Secrets = src, src
	})
	c.execPool = store.PoolByNode
	c.addAgent("agent-dc1", "dc1")
	return c
}

func ranOn(t *testing.T, tree store.RunTree) string {
	t.Helper()
	return tree.Steps[0].Executions[0].AgentID
}

// A step on a node in dc1 runs on the dc1 agent even though the default-pool
// agent may claim it first; a node without a pool, and a step without a node,
// run in the default pool.
func TestStepRoutedToThePoolOfItsNode(t *testing.T) {
	for name, tc := range map[string]struct {
		plugin, input string
		agent         string
	}{
		"node in dc1":        {"nodedemo", `{"node":"n1","token":"secret://dev/token"}`, "agent-dc1"},
		"node without pool":  {"nodedemo", `{"node":"n3","token":"secret://dev/token"}`, "agent-1"},
		"no node":            {"demo", `{"word":"hi"}`, "agent-1"},
		"two nodes one pool": {"twonodes", `{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n4"}}}`, "agent-dc1"},
		"pool and no pool":   {"twonodes", `{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n3"}}}`, "agent-dc1"},
	} {
		t.Run(name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				c := routeCluster(t, st, newPoolSources())
				tree := c.waitRun(c.triggerInput(tc.plugin, tc.input), store.RunSucceeded)
				if got := ranOn(t, tree); got != tc.agent {
					t.Fatalf("ran on %q, want %q", got, tc.agent)
				}
			})
		})
	}
}

// Nodes in two pools need an explicit runs_on.pool; the failure says so.
func TestStepWithNodesInTwoPoolsFails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := routeCluster(t, st, newPoolSources())
		tree := c.waitRun(c.triggerInput("twonodes", `{"source":{"node":{"name":"n1"}},"destination":{"node":{"name":"n2"}}}`), store.RunFailed)
		ex := tree.Steps[0].Executions[0]
		if ex.State != store.ExecFailed || !strings.Contains(ex.Error, "dc1, dc2") || !strings.Contains(ex.Error, "runs_on.pool") {
			t.Fatalf("execution = %s %q", ex.State, ex.Error)
		}
	})
}

// An unknown node fails for good, whichever pool's agent claimed the step, and
// no secret is read for a step that is routed away.
func TestRoutingFailsUnknownNodesAndReadsNoSecretsWhenRouting(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		src := newPoolSources()
		c := routeCluster(t, st, src)
		tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"missing","token":"secret://dev/token"}`), store.RunFailed)
		if ex := tree.Steps[0].Executions[0]; !strings.Contains(ex.Error, "unknown node") {
			t.Fatalf("execution = %s %q", ex.State, ex.Error)
		}
		src.mu.Lock()
		before := src.secrets
		src.mu.Unlock()
		tree = c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`), store.RunSucceeded)
		if ranOn(t, tree) != "agent-dc1" {
			t.Fatalf("ran on %q", ranOn(t, tree))
		}
		// The dc1 agent reads the node credential and the token for its own delivery; the
		// default agent that claimed first must not have read any.
		src.mu.Lock()
		got := src.secrets - before
		src.mu.Unlock()
		if got != 2 {
			t.Fatalf("secret reads for the routed step = %d, want 2 (node credential and token, read only by the final agent)", got)
		}
	})
}

// Without a PoolSource every step goes to the default pool.
func TestNodeSourceWithoutPoolsUsesDefaultPool(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, nodeSpec, withSources(&fakeSources{}))
		c.execPool = store.PoolByNode
		tree := c.waitRun(c.triggerInput("nodedemo", `{"node":"n1","token":"secret://dev/token"}`), store.RunSucceeded)
		if ranOn(t, tree) != "agent-1" {
			t.Fatalf("ran on %q", ranOn(t, tree))
		}
	})
}
