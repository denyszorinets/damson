package engine

import "strings"

// Graph node kinds.
const (
	NodeStart   = "start"
	NodeEnd     = "end"
	NodeStep    = "step"
	NodeGroup   = "group"
	NodeCatch   = "catch"
	NodeFinally = "finally"
)

// Graph edge kinds.
const (
	// EdgeSequence joins consecutive steps of a list.
	EdgeSequence = "sequence"
	// EdgeSkip bypasses a step whose `when` is not the default: the condition
	// decides whether it runs.
	EdgeSkip = "skip"
	// EdgeCatch leads from a group's body to its catch list, on failure.
	EdgeCatch = "catch"
	// EdgeFinally leads to a group's finally list, which always runs.
	EdgeFinally = "finally"
)

// Graph ids that are not step paths. `$` cannot start a step id.
const (
	StartNodeID = "$start"
	EndNodeID   = "$end"
	// laneTemplate stands for any lane key in a node id: `*` is not a valid key,
	// so `g/for_each[*]/x` is the template of `g/for_each[<key>]/x`.
	laneTemplate = "*"
)

// GraphForEach is the lane configuration of a for_each group.
type GraphForEach struct {
	Expr          string `json:"expr"`
	MaxParallel   int    `json:"max_parallel,omitempty"`
	FailThreshold int    `json:"fail_threshold,omitempty"`
}

// GraphNode is one box of the job graph. The node id is the instance path of the
// step it stands for (see GraphNodeOf for lanes); it carries no `with` input,
// so a graph never holds step input or anything secret.
type GraphNode struct {
	ID               string        `json:"id"`
	Kind             string        `json:"kind"`
	Parent           string        `json:"parent,omitempty"`
	Label            string        `json:"label"`
	Plugin           string        `json:"plugin,omitempty"`
	When             string        `json:"when,omitempty"`
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
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}

// Graph is the structure of a job: what its steps are and how control moves
// between them. It does not depend on any run, so it never changes while a run
// goes on; a run's progress is an overlay keyed by node (and lane).
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// BuildGraph builds the graph of a job. It is pure and deterministic: nodes
// are listed depth first in spec order, edges in the order they are found.
func BuildGraph(job *Job) Graph {
	b := &graphBuilder{}
	b.add(GraphNode{ID: StartNodeID, Kind: NodeStart, Label: "start"})
	steps := job.Spec.Steps
	if len(steps) > 0 {
		b.edge(StartNodeID, steps[0].ID, EdgeSequence, "")
	}
	b.list(steps, "", "", StartNodeID, EndNodeID)
	if len(steps) > 0 {
		b.edge(steps[len(steps)-1].ID, EndNodeID, EdgeSequence, "")
	}
	b.add(GraphNode{ID: EndNodeID, Kind: NodeEnd, Label: "end"})
	return Graph{Nodes: b.nodes, Edges: b.edges}
}

type graphBuilder struct {
	nodes []GraphNode
	edges []GraphEdge
}

func (b *graphBuilder) add(n GraphNode) { b.nodes = append(b.nodes, n) }

func (b *graphBuilder) edge(from, to, kind, label string) {
	b.edges = append(b.edges, GraphEdge{From: from, To: to, Kind: kind, Label: label})
}

// list adds the steps of one list. prefix is the path prefix of its instances
// ("" at the top, "g/" in a body, "g/for_each[*]/" in a lane body); parent is
// the node they belong to; head and tail are the nodes before and after the
// list, which the skip edges of its first and last step connect to.
func (b *graphBuilder) list(steps []Step, prefix, parent, head, tail string) {
	ids := make([]string, len(steps))
	for i, s := range steps {
		ids[i] = ChildPath(prefix, s.ID)
	}
	for i, s := range steps {
		b.step(s, ids[i], prefix, parent)
		before, after := head, tail
		if i > 0 {
			before = ids[i-1]
			b.edge(ids[i-1], ids[i], EdgeSequence, "")
		}
		if i < len(steps)-1 {
			after = ids[i+1]
		}
		if s.When != "" && s.When != "success()" {
			b.edge(before, after, EdgeSkip, s.When)
		}
	}
}

func (b *graphBuilder) step(s Step, id, prefix, parent string) {
	n := GraphNode{ID: id, Parent: parent, Label: s.ID, When: nonDefaultWhen(s.When), ContinueOnError: s.ContinueOnError, Timeout: s.Timeout}
	if !s.IsGroup() {
		n.Kind, n.Plugin = NodeStep, s.Plugin
		if s.Retry != nil {
			n.RetryMaxAttempts = s.Retry.MaxAttempts
		}
		b.add(n)
		return
	}
	n.Kind, n.Rethrow = NodeGroup, s.Rethrow != nil && *s.Rethrow
	body := id + "/"
	if s.ForEach != "" {
		n.ForEach = &GraphForEach{Expr: s.ForEach}
		if s.MaxParallel != nil {
			n.ForEach.MaxParallel = *s.MaxParallel
		}
		if s.FailThreshold != nil {
			n.ForEach.FailThreshold = *s.FailThreshold
		}
		body = LanePath(id, laneTemplate) + "/"
	}
	b.add(n)
	b.list(s.Steps, body, id, id, id)
	var last string
	if len(s.Steps) > 0 {
		last = ChildPath(body, s.Steps[len(s.Steps)-1].ID)
	}
	catch, finally := body+SegCatch, body+SegFinally
	if len(s.Catch) > 0 {
		b.add(GraphNode{ID: catch, Kind: NodeCatch, Parent: id, Label: SegCatch})
		b.list(s.Catch, catch+"/", catch, catch, catch)
		if last != "" {
			b.edge(last, catch, EdgeCatch, "on failure")
		}
	}
	if len(s.Finally) > 0 {
		b.add(GraphNode{ID: finally, Kind: NodeFinally, Parent: id, Label: SegFinally})
		b.list(s.Finally, finally+"/", finally, finally, finally)
		if len(s.Catch) > 0 {
			b.edge(catch, finally, EdgeFinally, "always")
		}
		if last != "" {
			b.edge(last, finally, EdgeFinally, "always")
		}
	}
}

func nonDefaultWhen(w string) string {
	if w == "success()" {
		return ""
	}
	return w
}

// GraphNodeOf maps the path of a step instance to the graph node it is an
// instance of, and to its lane ("" outside a for_each). A lane's own row
// ("g/for_each[web-01]") maps to the group's node; the instances inside a lane
// map to the template ("g/for_each[*]/x"), so every instance has a node and a
// graph does not grow with the number of lanes.
func GraphNodeOf(path string) (node, lane string) {
	segs := strings.Split(path, "/")
	for i := 1; i < len(segs); i++ {
		key, ok := laneKey(segs[i])
		if !ok {
			continue
		}
		if i == len(segs)-1 { // the lane row
			return strings.Join(segs[:i], "/"), key
		}
		segs[i] = laneSegPrefix + laneTemplate + "]"
		return strings.Join(segs, "/"), key
	}
	return path, ""
}
