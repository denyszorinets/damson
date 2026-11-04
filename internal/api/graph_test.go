package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/store"
)

const graphSpec = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"graph"},
"spec":{"steps":[
 {"id":"g","type":"group","for_each":"[\"a\",\"b\"]","max_parallel":1,
  "steps":[{"id":"x","plugin":"ssh","with":{"host":"h"},"retry":{"max_attempts":2}}],
  "catch":[{"id":"fix","plugin":"ssh","with":{"host":"h"}}],
  "finally":[{"id":"tidy","plugin":"ssh","with":{"host":"h"}}]},
 {"id":"prep","plugin":"ssh","with":{"host":"h"},"when":"1 == 1"}
]}}`

type graphBody struct {
	Nodes []struct {
		ID, Kind, Parent, Label, When string
		RetryMaxAttempts              int `json:"retry_max_attempts"`
		ForEach                       *struct {
			Expr        string
			MaxParallel int `json:"max_parallel"`
		} `json:"for_each"`
	}
	Edges []struct{ From, To, Kind, Label string }
}

func (g graphBody) node(id string) (kind string, ok bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n.Kind, true
		}
	}
	return "", false
}

func TestJobVersionGraph(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job := f.createJob(t, graphSpec)
		var g graphBody
		f.callOK(t, http.MethodGet, "/api/v1/jobs/"+job.Job.ID+"/versions/1/graph", "", http.StatusOK, &g)
		for id, kind := range map[string]string{
			"$start": "start", "prep": "step", "g": "group", "g/for_each[*]/x": "step",
			"g/for_each[*]/catch": "catch", "g/for_each[*]/catch/fix": "step",
			"g/for_each[*]/finally": "finally", "g/for_each[*]/finally/tidy": "step", "$end": "end",
		} {
			got, ok := g.node(id)
			assert.True(t, ok, "node %s missing", id)
			assert.Equal(t, kind, got, id)
		}
		for _, n := range g.Nodes {
			switch n.ID {
			case "prep":
				assert.Equal(t, "1 == 1", n.When)
			case "g":
				require.NotNil(t, n.ForEach)
				assert.Equal(t, 1, n.ForEach.MaxParallel)
			case "g/for_each[*]/x":
				assert.Equal(t, 2, n.RetryMaxAttempts)
			}
		}
		var kinds []string
		for _, e := range g.Edges {
			kinds = append(kinds, e.Kind)
		}
		assert.Contains(t, kinds, "skip")
		assert.Contains(t, kinds, "catch")
		assert.Contains(t, kinds, "finally")

		assert.Equal(t, http.StatusNotFound, f.call(t, http.MethodGet, "/api/v1/jobs/"+job.Job.ID+"/versions/9/graph", "").Code)
		assert.Equal(t, http.StatusNotFound, f.call(t, http.MethodGet, "/api/v1/jobs/nope/versions/1/graph", "").Code)
	})
}

func TestValidateReturnsTheGraphOnlyWhenValid(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		var ok struct {
			Valid bool       `json:"valid"`
			Graph *graphBody `json:"graph"`
		}
		f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(graphSpec), http.StatusOK, &ok)
		require.True(t, ok.Valid)
		require.NotNil(t, ok.Graph)
		assert.NotEmpty(t, ok.Graph.Nodes)

		var bad struct {
			Valid bool       `json:"valid"`
			Graph *graphBody `json:"graph"`
		}
		f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(`{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"x"},"spec":{"steps":[]}}`), http.StatusOK, &bad)
		assert.False(t, bad.Valid)
		assert.Nil(t, bad.Graph)
	})
}

// A run's graph comes with its step instances keyed by node and lane: the
// lanes of a for_each share the template node.
func TestRunGraphCarriesNodeAndLane(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job := f.createJob(t, graphSpec)
		run := f.trigger(t, job.Job.ID, `{}`)
		var rg struct {
			Graph graphBody
			Steps []struct {
				Path, Node, Lane, Status string
			}
		}
		f.callOK(t, http.MethodGet, "/api/v1/runs/"+run.ID+"/graph", "", http.StatusOK, &rg)
		require.NotEmpty(t, rg.Graph.Nodes)
		lanes := map[string]bool{}
		for _, s := range rg.Steps {
			if _, ok := rg.Graph.node(s.Node); !ok {
				t.Errorf("step %q has node %q, which the graph lacks", s.Path, s.Node)
			}
			if s.Path == "g/for_each[a]" {
				assert.Equal(t, "g", s.Node, "a lane row belongs to its group's node")
				assert.Equal(t, "a", s.Lane)
			}
			if s.Node == "g/for_each[*]/x" {
				lanes[s.Lane] = true
			}
		}
		assert.Equal(t, map[string]bool{"a": true}, lanes, "only the first lane starts (max_parallel 1); steps: %+v", rg.Steps)
		assert.Equal(t, http.StatusNotFound, f.call(t, http.MethodGet, "/api/v1/runs/nope/graph", "").Code)
	})
}

// A stored version the engine can no longer parse answers 500, not a graph.
func TestGraphOfAnUnparsableStoredVersionIs500(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job, _, err := st.CreateJob(context.Background(), store.NewJob{
			ProjectID: store.DefaultProjectID, Name: "broken", Spec: json.RawMessage(`{"apiVersion":"nope"}`), Checksum: "x",
		}, t0)
		require.NoError(t, err)
		assert.Equal(t, http.StatusInternalServerError, f.call(t, http.MethodGet, "/api/v1/jobs/"+job.ID+"/versions/1/graph", "").Code)
	})
}

// A spec that parses but whose plugin input is wrong is invalid: no graph.
func TestValidateNoGraphForAnInvalidPluginInput(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		var out struct {
			Valid  bool       `json:"valid"`
			Errors []struct{} `json:"errors"`
			Graph  *graphBody `json:"graph"`
		}
		f.callOK(t, http.MethodPost, "/api/v1/jobs/validate", wrapSpec(specDoc("badinput", "", "ssh", `{"host":123}`)), http.StatusOK, &out)
		assert.False(t, out.Valid)
		assert.NotEmpty(t, out.Errors)
		assert.Nil(t, out.Graph)
	})
}
