package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/denyszorinets/damson/internal/expr"
)

// TestBuildGraphGolden builds the graph of the job named by every
// testdata/engine/graph-*.input.json and compares it with the .golden.json.
// Refresh with -update.
func TestBuildGraphGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join(testdata, "graph-*.input.json"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".input.json")
		t.Run(name, func(t *testing.T) {
			var f struct {
				JobFile string `json:"job_file"`
			}
			if err := json.Unmarshal(mustRead(t, in), &f); err != nil {
				t.Fatal(err)
			}
			job, err := Parse(readFixture(t, f.JobFile))
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.MarshalIndent(BuildGraph(job), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			golden := strings.TrimSuffix(in, ".input.json") + ".golden.json"
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if want := mustRead(t, golden); !bytes.Equal(got, want) {
				t.Fatalf("graph differs from %s (run with -update to refresh)\ngot:\n%s\nwant:\n%s", golden, got, want)
			}
		})
	}
}

// Every valid job of the fixtures yields a well-formed graph: unique node ids,
// edges between existing nodes, parents that exist, and no input (`with`) in
// any node.
func TestBuildGraphWellFormed(t *testing.T) {
	jobs, _ := filepath.Glob(filepath.Join(testdata, "job-*.json"))
	built := 0
	for _, p := range jobs {
		job, err := Parse(mustRead(t, p))
		if err != nil {
			continue // the invalid-job fixtures
		}
		built++
		g := BuildGraph(job)
		ids := map[string]bool{}
		for _, n := range g.Nodes {
			if ids[n.ID] {
				t.Errorf("%s: duplicate node %q", p, n.ID)
			}
			ids[n.ID] = true
		}
		for _, n := range g.Nodes {
			if n.Parent != "" && !ids[n.Parent] {
				t.Errorf("%s: node %q has no parent %q", p, n.ID, n.Parent)
			}
		}
		for _, e := range g.Edges {
			if !ids[e.From] || !ids[e.To] {
				t.Errorf("%s: edge %+v joins a missing node", p, e)
			}
		}
		again, _ := json.Marshal(BuildGraph(job))
		first, _ := json.Marshal(g)
		if !bytes.Equal(first, again) {
			t.Errorf("%s: BuildGraph is not deterministic", p)
		}
	}
	if built < 20 {
		t.Fatalf("only %d jobs built; the fixtures went missing", built)
	}
}

// No instance row without a node: every step instance path of every decide
// fixture's state (and every path Decide creates) maps to a node of its job's
// graph.
func TestEveryInstanceHasANode(t *testing.T) {
	inputs, _ := filepath.Glob(filepath.Join(testdata, "decide-*.input.json"))
	eng := newEngine(t, expr.Limits{})
	for _, in := range inputs {
		job, st, now := loadFixture(t, in)
		nodes := map[string]bool{}
		for _, n := range BuildGraph(job).Nodes {
			nodes[n.ID] = true
		}
		check := func(path string) {
			if node, _ := GraphNodeOf(path); !nodes[node] {
				t.Errorf("%s: instance %q maps to %q, which is not a node", filepath.Base(in), path, node)
			}
		}
		for _, s := range st.Steps {
			check(s.Path)
		}
		acts, err := eng.Decide(job, st, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range acts {
			if c, ok := a.(CreateStepInstance); ok {
				check(c.Path)
			}
		}
	}
}

func TestGraphNodeOf(t *testing.T) {
	tests := []struct{ path, node, lane string }{
		{"a", "a", ""},
		{"g/x", "g/x", ""},
		{"g/catch/x", "g/catch/x", ""},
		{"g/finally/x", "g/finally/x", ""},
		{"g/for_each[web-01]", "g", "web-01"},
		{"g/for_each[web-01]/x", "g/for_each[*]/x", "web-01"},
		{"g/for_each[3]/catch/x", "g/for_each[*]/catch/x", "3"},
		{"g/for_each[a.b]/h/x", "g/for_each[*]/h/x", "a.b"},
		{"o/h/for_each[k]/x", "o/h/for_each[*]/x", "k"},
		{"finally", "finally", ""},
	}
	for _, tc := range tests {
		if node, lane := GraphNodeOf(tc.path); node != tc.node || lane != tc.lane {
			t.Errorf("GraphNodeOf(%q) = (%q, %q); want (%q, %q)", tc.path, node, lane, tc.node, tc.lane)
		}
	}
}

// A graph never carries step input.
func TestGraphHasNoStepInput(t *testing.T) {
	job, err := Parse(readFixture(t, "job-pipeline.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(BuildGraph(job))
	if bytes.Contains(b, []byte(`"with"`)) || bytes.Contains(b, []byte("secret://")) {
		t.Fatalf("graph carries step input: %s", b)
	}
}
