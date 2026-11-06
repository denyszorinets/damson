package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

type nodePage struct {
	Items      []api.Node `json:"items"`
	NextCursor string     `json:"next_cursor"`
}

func TestNodesSelectorPreview(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		for i := 0; i < 5; i++ {
			os := "linux"
			if i%2 == 1 {
				os = "windows"
			}
			f.callJSON(t, "POST", "/api/v1/nodes", fmt.Sprintf(`{"name":"n%d","address":"h%d","os":%q}`, i, i, os), 201, nil)
		}
		var p nodePage
		f.callJSON(t, "GET", "/api/v1/nodes?selector=node.os+%3D%3D+%22windows%22", "", 200, &p)
		require.Len(t, p.Items, 2)
		assert.Equal(t, "n1", p.Items[0].Name)
		assert.Empty(t, p.NextCursor)

		// A full page resumes after the last name looked at.
		f.callJSON(t, "GET", "/api/v1/nodes?limit=2&selector=node.os+%3D%3D+%22linux%22", "", 200, &p)
		require.Len(t, p.Items, 2)
		require.NotEmpty(t, p.NextCursor)
		var next nodePage
		f.callJSON(t, "GET", "/api/v1/nodes?limit=2&selector=node.os+%3D%3D+%22linux%22&cursor="+p.NextCursor, "", 200, &next)
		require.Len(t, next.Items, 1)
		assert.Equal(t, "n4", next.Items[0].Name)

		// A selector that does not compile is a 422 at query.selector.
		rec := f.callJSON(t, "GET", "/api/v1/nodes?selector=node.os+%3D%3D", "", http.StatusUnprocessableEntity, nil)
		assert.Contains(t, rec.Body.String(), "query.selector")
	})
}

func TestRunTargetsEndpoint(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		job, ver, err := st.CreateJob(t.Context(), store.NewJob{ProjectID: store.DefaultProjectID, Name: "rt", Spec: []byte(`{}`), Checksum: "c"}, t0)
		require.NoError(t, err)
		tree, err := st.CreateRun(t.Context(), store.NewRun{JobID: job.ID, JobVersionID: ver.ID, Targets: []store.RunTarget{
			{Name: "a", NodeID: "id-a", Node: []byte(`{"os":"linux","pool":"p","transport":"ssh","address":"h","tags":["x"],"attributes":{"k":1}}`)},
			{Name: "b", NodeID: "id-b", Node: []byte(`{}`)},
			{Name: "c", NodeID: "id-c", Node: []byte(`{}`)},
		}}, t0)
		require.NoError(t, err)

		for _, tok := range []string{"", "dma_nope"} {
			assert.Equal(t, http.StatusUnauthorized, f.do("GET", "/api/v1/runs/"+tree.ID+"/targets", "", tok).Code)
		}
		var out struct {
			Items      []api.RunTarget `json:"items"`
			NextCursor string          `json:"next_cursor"`
		}
		f.callJSON(t, "GET", "/api/v1/runs/"+tree.ID+"/targets?limit=2", "", 200, &out)
		require.Len(t, out.Items, 2)
		assert.Equal(t, "id-a", out.Items[0].NodeID)
		assert.Equal(t, "linux", out.Items[0].OS)
		assert.Equal(t, []string{"x"}, out.Items[0].Tags)
		assert.Equal(t, []string{}, out.Items[1].Tags)
		require.NotEmpty(t, out.NextCursor)
		cur := out.NextCursor
		out.NextCursor = ""
		f.callJSON(t, "GET", "/api/v1/runs/"+tree.ID+"/targets?limit=2&cursor="+cur, "", 200, &out)
		require.Len(t, out.Items, 1)
		assert.Equal(t, "c", out.Items[0].Name)
		assert.Empty(t, out.NextCursor)

		f.callJSON(t, "GET", "/api/v1/runs/0192a000-0000-7000-8000-00000000ffff/targets", "", http.StatusNotFound, nil)
	})
}

func TestForgetObservedHostKey(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		var n api.Node
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"web","address":"h","host_key_policy":"tofu"}`, 201, &n)
		job, ver, err := st.CreateJob(t.Context(), store.NewJob{ProjectID: store.DefaultProjectID, Name: "fk", Spec: []byte(`{}`), Checksum: "c"}, t0)
		require.NoError(t, err)
		_, err = st.CreateRun(t.Context(), store.NewRun{JobID: job.ID, JobVersionID: ver.ID,
			Steps: []store.NewStepInstance{{Path: "s", Plugin: "ssh", Execution: &store.NewExecution{Pool: "default", ScheduledAt: t0}}}}, t0)
		require.NoError(t, err)
		claimed, err := st.ClaimExecutions(t.Context(), store.ClaimParams{AgentID: "a1", ControllerID: "c", Pools: []string{"default"}, Limit: 1, LeaseTTL: time.Hour}, t0)
		require.NoError(t, err)
		require.Len(t, claimed, 1)
		ok, err := st.ObserveNodeHostKey(t.Context(), n.ID, "ssh-ed25519 AAAA", claimed[0].AttemptID, "a1", t0)
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, http.StatusUnauthorized, f.do("DELETE", "/api/v1/nodes/web/host-key", "", "").Code)
		f.callJSON(t, "GET", "/api/v1/nodes/web", "", 200, &n)
		assert.Equal(t, "ssh-ed25519 AAAA", n.HostKeyObserved)
		f.callJSON(t, "DELETE", "/api/v1/nodes/web/host-key", "", http.StatusNoContent, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/web", "", 200, &n)
		assert.Empty(t, n.HostKeyObserved)
		f.callJSON(t, "DELETE", "/api/v1/nodes/nope/host-key", "", http.StatusNotFound, nil)
	})
}
