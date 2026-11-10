package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func (f *secretsFixture) callJSON(t *testing.T, method, target, body string, want int, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := f.do(method, target, body, f.admin)
	require.Equal(t, want, rec.Code, "%s %s: %s", method, target, rec.Body.String())
	if out != nil {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), out))
	}
	return rec
}

func TestNodesRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		pool, _, _ := tokens.New()
		body := `{"name":"web","address":"10.0.0.1"}`
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			for _, r := range [][3]string{
				{"GET", "/api/v1/nodes", ""}, {"GET", "/api/v1/nodes/web", ""}, {"POST", "/api/v1/nodes", body},
				{"PATCH", "/api/v1/nodes/web", `{}`}, {"DELETE", "/api/v1/nodes/web", ""},
			} {
				assert.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], r[2], tok).Code, "%s %s", r[0], r[1])
			}
		}
	})
}

func TestNodesCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		var n api.Node
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"web-1","address":"web-1.example.com","tags":["prod","db","prod"],"attributes":{"env":"prod","n":3},"credential":"secret://_inventory/web-key"}`, 201, &n)
		assert.Equal(t, 22, n.Port)
		assert.Equal(t, "ssh", n.Transport)
		assert.Equal(t, "strict", n.HostKeyPolicy)
		assert.Equal(t, "manual", n.Source)
		assert.True(t, n.Enabled)
		assert.EqualValues(t, 1, n.Version)
		assert.Equal(t, []string{"db", "prod"}, n.Tags)
		assert.Equal(t, "secret://_inventory/web-key", n.Credential)
		assert.EqualValues(t, 3, n.Attributes["n"])

		var w api.Node
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"win-1","address":"10.0.0.2","transport":"winrm"}`, 201, &w)
		assert.Equal(t, 5986, w.Port)

		// Conflicts and validation.
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"web-1","address":"10.0.0.9"}`, 409, nil)
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"WEB-1","address":"10.0.0.9"}`, 409, nil)
		var p problemBody
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"a/b","address":"host:22","credential":"hunter2","port":70000}`, 422, &p)
		assert.ElementsMatch(t, []string{"body.name", "body.address", "body.credential", "body.port"}, p.locations())
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"x","address":"10.0.0.9","source":"sync1"}`, 422, nil)
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"x","address":"10.0.0.9","version":9}`, 422, nil)

		// Read, list and paging.
		f.callJSON(t, "GET", "/api/v1/nodes/web-1", "", 200, &n)
		f.callJSON(t, "GET", "/api/v1/nodes/nope", "", 404, nil)
		var page struct {
			Items      []api.Node `json:"items"`
			NextCursor string     `json:"next_cursor"`
		}
		f.callJSON(t, "GET", "/api/v1/nodes?limit=1", "", 200, &page)
		require.Len(t, page.Items, 1)
		require.NotEmpty(t, page.NextCursor)
		assert.Equal(t, "web-1", page.Items[0].Name)
		f.callJSON(t, "GET", "/api/v1/nodes?limit=5&cursor="+page.NextCursor, "", 200, &page)
		require.Len(t, page.Items, 1)
		assert.Equal(t, "win-1", page.Items[0].Name)
		f.callJSON(t, "GET", "/api/v1/nodes?enabled=false", "", 200, &page)
		assert.Empty(t, page.Items)

		// Patch: partial, with a stale version, clearing, and the immutable fields.
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"if_version":1,"address":"10.9.9.9","enabled":false,"credential":""}`, 200, &n)
		assert.Equal(t, "10.9.9.9", n.Address)
		assert.False(t, n.Enabled)
		assert.Empty(t, n.Credential)
		assert.Equal(t, []string{"db", "prod"}, n.Tags, "unpatched fields stay")
		assert.EqualValues(t, 2, n.Version)
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"if_version":1,"os":"linux"}`, 409, &p)
		assert.Contains(t, p.Type, "stale")
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"os":"linux"}`, 200, &n) // no if_version: applies to the current one
		assert.EqualValues(t, 3, n.Version)
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"name":"other"}`, 422, nil)
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"source":"sync1"}`, 422, nil)
		f.callJSON(t, "PATCH", "/api/v1/nodes/web-1", `{"port":0}`, 422, nil)
		f.callJSON(t, "PATCH", "/api/v1/nodes/nope", `{"os":"linux"}`, 404, nil)

		// Delete.
		f.callJSON(t, "DELETE", "/api/v1/nodes/web-1?if_version=1", "", 409, nil)
		f.callJSON(t, "DELETE", "/api/v1/nodes/web-1", "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/nodes/web-1", "", 404, nil)
	})
}

// A node owned by a sync source is changed only with force, and force keeps
// the source (the next sync overwrites the edit).
func TestNodesSourceConflict(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		_, err := st.CreateNode(t.Context(), store.Node{
			ID: "n-sync", Name: "synced", Address: "10.0.0.5", Port: 22, Transport: "ssh", HostKeyPolicy: "strict",
			Source: "sync1", ExternalID: "ext-1", Enabled: true,
		}, t0)
		require.NoError(t, err)
		var p problemBody
		f.callJSON(t, "PATCH", "/api/v1/nodes/synced", `{"os":"linux"}`, 409, &p)
		assert.Equal(t, "urn:damson:problem:inventory-source-conflict", p.Type)
		f.callJSON(t, "DELETE", "/api/v1/nodes/synced", "", 409, &p)
		assert.Equal(t, "urn:damson:problem:inventory-source-conflict", p.Type)

		var n api.Node
		f.callJSON(t, "PATCH", "/api/v1/nodes/synced?force=true", `{"os":"linux"}`, 200, &n)
		assert.Equal(t, "linux", n.OS)
		assert.Equal(t, "sync1", n.Source)
		assert.Equal(t, "ext-1", n.ExternalID)
		var page struct {
			Items []api.Node `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/nodes?source=sync1", "", 200, &page)
		assert.Len(t, page.Items, 1)
		f.callJSON(t, "GET", "/api/v1/nodes?source=manual", "", 200, &page)
		assert.Empty(t, page.Items)
		f.callJSON(t, "DELETE", "/api/v1/nodes/synced?force=true", "", 204, nil)
	})
}

func TestNodeResponsesCarryNoSecretValues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		rec := f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"n","address":"10.0.0.1","credential":"secret://_inventory/k"}`, 201, nil)
		// The credential is a reference; the page never resolves it.
		assert.Contains(t, rec.Body.String(), `"credential":"secret://_inventory/k"`)
		assert.False(t, strings.Contains(rec.Body.String(), "password"))
	})
}

// racyStore moves the node under the handler once, between its read and write.
type racyStore struct {
	store.Store
	mu    sync.Mutex
	reads int
	every bool // bump on every read, so retries never win
}

func (r *racyStore) GetNode(ctx context.Context, name string) (store.Node, error) {
	n, err := r.Store.GetNode(ctx, name)
	r.mu.Lock()
	r.reads++
	bump := err == nil && (r.every || r.reads == 1)
	r.mu.Unlock()
	if bump {
		moved := n
		moved.Pool = "raced"
		_, _ = r.UpdateNode(ctx, moved, n.Version, t0)
	}
	return n, err
}

// A patch without if_version that loses a race merges again and keeps both
// edits; one that always loses gives up with a stale 409.
func TestNodePatchRetriesWithoutIfVersion(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		r := &racyStore{Store: st}
		f := newSecretsFixture(t, r, false)
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"n","address":"10.0.0.1"}`, 201, nil)
		r.mu.Lock()
		r.reads = 0
		r.mu.Unlock()
		var n api.Node
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"os":"linux"}`, 200, &n)
		assert.Equal(t, "linux", n.OS)
		assert.Equal(t, "raced", n.Pool, "the concurrent edit survives")
		assert.EqualValues(t, 3, n.Version)

		r.mu.Lock()
		r.every = true
		r.mu.Unlock()
		var p problemBody
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"os":"windows"}`, 409, &p)
		assert.Contains(t, p.Type, "stale")
	})
}

func TestNodePatchClearingNoOpAndTransport(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		var n api.Node
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"n","address":"10.0.0.1","pool":"p","tags":["a"],"attributes":{"k":"v"},"credential":"secret://_inventory/k"}`, 201, &n)
		n = api.Node{} // decoding into a used value would merge the old attributes in
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"tags":[],"attributes":{},"pool":"","credential":""}`, 200, &n)
		assert.Empty(t, n.Tags)
		assert.Empty(t, n.Attributes)
		assert.Empty(t, n.Pool)
		assert.Empty(t, n.Credential)
		v := n.Version
		// null leaves a field alone, like leaving it out.
		n = api.Node{}
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"tags":null}`, 200, &n)
		assert.Equal(t, v, n.Version)
		// An empty or unchanged patch writes nothing.
		n = api.Node{}
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{}`, 200, &n)
		assert.Equal(t, v, n.Version)
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"address":"10.0.0.1"}`, 200, &n)
		assert.Equal(t, v, n.Version)
		// A port still at the old transport's default follows the transport.
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"transport":"winrm"}`, 200, &n)
		assert.Equal(t, 5986, n.Port)
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"transport":"ssh","port":2222}`, 200, &n)
		f.callJSON(t, "PATCH", "/api/v1/nodes/n", `{"transport":"winrm"}`, 200, &n)
		assert.Equal(t, 2222, n.Port, "a chosen port stays")
	})
}
