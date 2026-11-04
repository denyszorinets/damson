package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/denyszorinets/damson/internal/engine"
)

// GraphForEach is the lane configuration of a for_each group node.
type GraphForEach struct {
	Expr          string `json:"expr" doc:"CEL expression that yields the lanes; evaluated once when the group starts."`
	MaxParallel   int    `json:"max_parallel,omitempty"`
	FailThreshold int    `json:"fail_threshold,omitempty"`
}

// GraphNode is one box of the job graph. Its id is the path of the step it
// stands for; instances inside a for_each lane share the template node
// "<group>/for_each[*]/<id>" and carry their lane on the instance. Nodes hold
// no step input.
type GraphNode struct {
	ID               string        `json:"id"`
	Kind             string        `json:"kind" enum:"start,end,step,group,catch,finally"`
	Parent           string        `json:"parent,omitempty" doc:"Id of the containing group, catch or finally node."`
	Label            string        `json:"label"`
	Plugin           string        `json:"plugin,omitempty"`
	When             string        `json:"when,omitempty" doc:"The step's condition when it is not the default success()."`
	ContinueOnError  bool          `json:"continue_on_error,omitempty"`
	RetryMaxAttempts int           `json:"retry_max_attempts,omitempty"`
	Timeout          string        `json:"timeout,omitempty"`
	ForEach          *GraphForEach `json:"for_each,omitempty"`
	Rethrow          bool          `json:"rethrow,omitempty"`
}

// GraphEdge joins two nodes.
type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Kind  string `json:"kind" enum:"sequence,skip,catch,finally" doc:"skip bypasses a step whose condition is not the default; catch leads from a group's body to its catch list on failure; finally leads to the finally list, which always runs."`
	Label string `json:"label,omitempty"`
}

// Graph is the structure of a job. It does not depend on a run: a run's
// progress is an overlay of step states keyed by node and lane.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

func toGraph(g engine.Graph) Graph {
	out := Graph{Nodes: make([]GraphNode, len(g.Nodes)), Edges: make([]GraphEdge, len(g.Edges))}
	for i, n := range g.Nodes {
		out.Nodes[i] = GraphNode{
			ID: n.ID, Kind: n.Kind, Parent: n.Parent, Label: n.Label, Plugin: n.Plugin, When: n.When,
			ContinueOnError: n.ContinueOnError, RetryMaxAttempts: n.RetryMaxAttempts, Timeout: n.Timeout, Rethrow: n.Rethrow,
		}
		if n.ForEach != nil {
			out.Nodes[i].ForEach = &GraphForEach{Expr: n.ForEach.Expr, MaxParallel: n.ForEach.MaxParallel, FailThreshold: n.ForEach.FailThreshold}
		}
	}
	for i, e := range g.Edges {
		out.Edges[i] = GraphEdge{From: e.From, To: e.To, Kind: e.Kind, Label: e.Label}
	}
	return out
}

// RunGraph is the graph of a run's pinned job version with the current state of
// its step instances. Live changes arrive on the run's event stream, keyed the
// same way (node and lane), so the graph itself is fetched once.
type RunGraph struct {
	Graph Graph       `json:"graph"`
	Steps []StepEvent `json:"steps" doc:"Step instances in creation order."`
}

type getGraphOutput struct{ Body Graph }

type getRunGraphOutput struct{ Body RunGraph }

func registerGraph(api huma.API, d deps) {
	huma.Register(api, huma.Operation{
		OperationID: "get-job-version-graph", Method: http.MethodGet, Path: "/jobs/{id}/versions/{v}/graph",
		Summary: "Get the graph of one job version", Tags: []string{"jobs"}, Errors: []int{http.StatusNotFound},
		Description: "Built by the server from the stored spec (engine.BuildGraph); clients lay it out and draw it and never derive control flow from the spec.",
	}, func(ctx context.Context, in *versionInput) (*getGraphOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		v, err := d.ds.GetJobVersionByNumber(ctx, in.ID, in.Version)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		g, err := d.graphOf(v.Spec)
		if err != nil {
			loggerFromCtx(ctx).Error("building the graph of a stored job version", "job_id", in.ID, "version", in.Version, "error", err)
			return nil, apiError(ctx, err)
		}
		return &getGraphOutput{Body: g}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "get-run-graph", Method: http.MethodGet, Path: "/runs/{id}/graph",
		Summary: "Get the graph of a run with the state of its steps", Tags: []string{"runs"}, Errors: []int{http.StatusNotFound},
	}, func(ctx context.Context, in *runIDInput) (*getRunGraphOutput, error) {
		if err := d.ready(); err != nil {
			return nil, apiError(ctx, err)
		}
		tree, err := d.ds.GetRunTree(ctx, in.ID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		ver, err := d.ds.GetJobVersion(ctx, tree.JobVersionID)
		if err != nil {
			return nil, apiError(ctx, err)
		}
		g, err := d.graphOf(ver.Spec)
		if err != nil {
			loggerFromCtx(ctx).Error("building the graph of a run", slog.String("run_id", in.ID), "error", err)
			return nil, apiError(ctx, err)
		}
		out := &getRunGraphOutput{Body: RunGraph{Graph: g, Steps: make([]StepEvent, 0, len(tree.Steps))}}
		for _, n := range tree.Steps {
			out.Body.Steps = append(out.Body.Steps, stepEvent(n))
		}
		return out, nil
	})
}

// graphOf builds the graph of a stored spec. A spec that no longer parses is an
// internal error: stored versions were validated when they were saved.
func (d deps) graphOf(spec []byte) (Graph, error) {
	job, err := d.eng.Parse(spec)
	if err != nil {
		return Graph{}, fmt.Errorf("parsing a stored job version: %w", err)
	}
	return toGraph(engine.BuildGraph(job)), nil
}
