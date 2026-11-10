package dispatch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/controller/dispatch"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// listPlugin is a TargetInventory plugin: it logs the nodes it received with
// their tags and whether each credential resolved.
type listPlugin struct{}

func (listPlugin) Manifest() pluginsdk.Manifest {
	return pluginsdk.Manifest{
		Name: "hostlist", Version: "1.0.0", Title: "hostlist", TargetMode: pluginsdk.TargetInventory,
		NodeListPath: "/hosts",
		InputSchema:  pluginsdk.MustSchemaFor[struct{}](), OutputSchema: pluginsdk.MustSchemaFor[struct{}](),
	}
}

func (listPlugin) Validate(context.Context, json.RawMessage) error { return nil }

func (listPlugin) Execute(_ context.Context, req pluginsdk.ExecRequest, emit pluginsdk.Emitter) (pluginsdk.Result, error) {
	var parts []string
	for _, name := range []string{"n1", "n2", "n4"} {
		n, ok := req.Nodes[name]
		if !ok {
			continue
		}
		if _, err := req.Secrets.ResolveFields(n.Credential); err != nil {
			return pluginsdk.Result{Error: &pluginsdk.StepError{Message: "credential missing for " + name}}, nil
		}
		parts = append(parts, name+"["+strings.Join(n.Tags, "+")+"]")
	}
	emit.Log(pluginsdk.Stdout, "hosts "+strings.Join(parts, ","))
	return pluginsdk.Result{Outputs: json.RawMessage(`{}`)}, nil
}

// tagSources adds tags to the pair sources and one more node, n4, that is
// disabled.
type tagSources struct{ pairSources }

func (p *tagSources) Node(ctx context.Context, run, name string) (pluginsdk.NodeInfo, error) {
	if name == "n4" {
		return pluginsdk.NodeInfo{}, dispatch.ErrNodeDisabled
	}
	n, err := p.pairSources.Node(ctx, run, name)
	n.Tags = []string{"web", name}
	return n, err
}

// batchTagSources answers lists through the batch interface, with the same
// answers tagSources gives one by one, and fails the test if a step of several
// names falls back to one-by-one lookups.
type batchTagSources struct {
	tagSources
	batches, singles int
}

func (b *batchTagSources) Node(ctx context.Context, run, name string) (pluginsdk.NodeInfo, error) {
	b.singles++
	return b.tagSources.Node(ctx, run, name)
}

func (b *batchTagSources) Nodes(ctx context.Context, run string, names []string) ([]dispatch.NodeResult, error) {
	b.batches++
	out := make([]dispatch.NodeResult, len(names))
	for i, name := range names {
		out[i].Info, out[i].Err = b.tagSources.Node(ctx, run, name)
	}
	return out, nil
}

// An inventory step receives every listed node, with its tags and credential;
// any node that cannot be used fails the whole step, and the list is checked
// before anything is sent.
func TestInventoryModeDeliversTheListedNodes(t *testing.T) {
	big := make([]string, pluginsdk.MaxNodeList+1)
	for i := range big {
		big[i] = "n1"
	}
	bigJSON, _ := json.Marshal(map[string]any{"hosts": big})
	for name, tc := range map[string]struct {
		input string
		log   string
		want  store.RunStatus
	}{
		"two hosts":       {`{"hosts":["n2","n1"]}`, "hosts n1[web+n1],n2[web+n2]", store.RunSucceeded},
		"duplicates":      {`{"hosts":["n1","n1"]}`, "hosts n1[web+n1]", store.RunSucceeded},
		"empty list":      {`{"hosts":[]}`, "hosts ", store.RunSucceeded},
		"unknown host":    {`{"hosts":["n1","n3"]}`, "", store.RunFailed},
		"disabled host":   {`{"hosts":["n1","n4"]}`, "", store.RunFailed},
		"list is missing": {`{}`, "", store.RunFailed},
		"not a list":      {`{"hosts":"n1"}`, "", store.RunFailed},
		"item not a name": {`{"hosts":["n1",7]}`, "", store.RunFailed},
		"too many":        {string(bigJSON), "", store.RunFailed},
	} {
		t.Run(name, func(t *testing.T) {
			forEachBackend(t, func(t *testing.T, st store.Store) {
				src := &batchTagSources{}
				c := startClusterPlugins(t, st, nodeSpec, []pluginsdk.Plugin{listPlugin{}}, func(cfg *dispatch.Config) {
					cfg.Nodes, cfg.Secrets = src, src
				})
				tree := c.waitRun(c.triggerInput("hostlist", tc.input), tc.want)
				if src.singles > 0 && !strings.Contains(tc.input, `["n1","n1"]`) && src.batches > 0 {
					// a step with several distinct names never mixes the two paths
					t.Errorf("%d one-by-one lookups next to %d batch lookups", src.singles, src.batches)
				}
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

// bigSources serves any node name with a large attribute blob, so a list of
// them cannot fit one protocol frame.
type bigSources struct{ fakeSources }

func (b *bigSources) Node(_ context.Context, _, name string) (pluginsdk.NodeInfo, error) {
	return pluginsdk.NodeInfo{
		Name: name, Address: "10.0.0.1", Credential: "secret://dev/cred", HostKeyPolicy: "strict",
		Attributes: map[string]any{"blob": strings.Repeat("x", 5000)},
	}, nil
}

// A request that cannot fit one frame fails the step for good, before
// anything is sent, and says nothing about the secrets it carried.
func TestInventoryRequestTooLargeFailsPermanently(t *testing.T) {
	hosts := make([]string, 1000)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%04d", i)
	}
	input, _ := json.Marshal(map[string]any{"hosts": hosts})
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startClusterPlugins(t, st, nodeSpec, []pluginsdk.Plugin{listPlugin{}}, func(cfg *dispatch.Config) {
			src := &bigSources{}
			cfg.Nodes, cfg.Secrets = src, src
		})
		tree := c.waitRun(c.triggerInput("hostlist", string(input)), store.RunFailed)
		ex := tree.Steps[0].Executions[0]
		if ex.State != store.ExecFailed || !strings.Contains(ex.Error, "step request is") || strings.Contains(ex.Error, secretMarker) {
			t.Fatalf("execution = %s %q", ex.State, ex.Error)
		}
	})
}
