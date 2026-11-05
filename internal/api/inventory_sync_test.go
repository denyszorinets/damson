package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func TestInventorySyncRequiresTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		pool, _, _ := tokens.New()
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			assert.Equal(t, http.StatusUnauthorized, f.do("PUT", "/api/v1/inventory/sources/cmdb/nodes", `{"nodes":[]}`, tok).Code)
			assert.Equal(t, http.StatusUnauthorized, f.do("POST", "/api/v1/inventory/sources/cmdb/nodes:batch", `{}`, tok).Code)
		}
	})
}

func TestInventoryFullStateSync(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		const url = "/api/v1/inventory/sources/cmdb/nodes"
		two := `{"nodes":[{"name":"a","external_id":"1","address":"10.0.0.1","tags":["x"]},{"name":"b","external_id":"2","address":"10.0.0.2","transport":"winrm","enabled":false}]}`
		var r api.SyncResult
		f.callJSON(t, "PUT", url, two, 200, &r)
		assert.Equal(t, []string{"a", "b"}, r.Created)
		assert.False(t, r.DryRun)
		var n api.Node
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 200, &n)
		assert.Equal(t, "cmdb", n.Source)
		assert.Equal(t, "1", n.ExternalID)
		assert.Equal(t, 22, n.Port)
		assert.True(t, n.Enabled)
		f.callJSON(t, "GET", "/api/v1/nodes/b", "", 200, &n)
		assert.Equal(t, 5986, n.Port)
		assert.False(t, n.Enabled)

		// Replay: unchanged, and no version moves.
		r = api.SyncResult{}
		f.callJSON(t, "PUT", url, two, 200, &r)
		assert.Equal(t, []string{"a", "b"}, r.Unchanged)
		assert.Empty(t, r.Created)
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 200, &n)
		assert.EqualValues(t, 1, n.Version)

		// A dry run reports and writes nothing; prune removes what left the body.
		one := `{"nodes":[{"name":"a","external_id":"1","address":"10.9.9.9"}]}`
		r = api.SyncResult{}
		f.callJSON(t, "PUT", url+"?dry_run=true&prune=true", one, 200, &r)
		assert.True(t, r.DryRun)
		assert.Equal(t, []string{"a"}, r.Updated)
		assert.Equal(t, []string{"b"}, r.Deleted)
		f.callJSON(t, "GET", "/api/v1/nodes/b", "", 200, nil)
		r = api.SyncResult{}
		f.callJSON(t, "PUT", url+"?prune=true", one, 200, &r)
		f.callJSON(t, "GET", "/api/v1/nodes/b", "", 404, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 200, &n)
		assert.Equal(t, "10.9.9.9", n.Address)
		assert.EqualValues(t, 2, n.Version)

		// Other sources are never touched; a name they hold is a conflict that writes nothing.
		f.callJSON(t, "POST", "/api/v1/nodes", `{"name":"m1","address":"10.1.1.1"}`, 201, nil)
		var p problemBody
		f.callJSON(t, "PUT", url+"?prune=true", `{"nodes":[{"name":"a","external_id":"1","address":"1.1.1.1"},{"name":"M1","address":"10.1.1.2"},{"name":"new","address":"10.1.1.3"}]}`, 409, &p)
		assert.Equal(t, "urn:damson:problem:inventory-sync-conflict", p.Type)
		require.Len(t, p.Errors, 1)
		assert.Equal(t, "node.M1", p.Errors[0].Location)
		assert.Contains(t, p.Errors[0].Message, "manual")
		f.callJSON(t, "GET", "/api/v1/nodes/new", "", 404, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/m1", "", 200, nil)

		// A synced node is edited through the nodes API only with force.
		f.callJSON(t, "PATCH", "/api/v1/nodes/a", `{"os":"linux"}`, 409, &p)
		assert.Equal(t, "urn:damson:problem:inventory-source-conflict", p.Type)
	})
}

func TestInventorySyncValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		var p problemBody
		for _, src := range []string{"manual", "devfile", "Bad", "has_underscore"} {
			f.callJSON(t, "PUT", "/api/v1/inventory/sources/"+src+"/nodes", `{"nodes":[]}`, 422, &p)
		}
		const url = "/api/v1/inventory/sources/cmdb/nodes"
		f.callJSON(t, "PUT", url, `{"nodes":[{"name":"a/b","address":"host:22"}]}`, 422, &p)
		assert.NotEmpty(t, p.Detail)
		f.callJSON(t, "PUT", url, `{"nodes":[{"name":"a","address":"h"},{"name":"A","address":"h2"}]}`, 422, nil) // case-only duplicate
		f.callJSON(t, "PUT", url, `{"nodes":[{"name":"a","address":"h","source":"manual"}]}`, 422, nil)           // unknown field
		f.callJSON(t, "PUT", url, `{}`, 422, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 404, nil) // nothing was written
	})
}

func TestInventoryBatch(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		const url = "/api/v1/inventory/sources/cmdb/nodes:batch"
		var r api.SyncResult
		f.callJSON(t, "POST", url, `{"upserts":[{"name":"a","external_id":"1","address":"10.0.0.1"},{"name":"b","external_id":"2","address":"10.0.0.2"}]}`, 200, &r)
		assert.Equal(t, []string{"a", "b"}, r.Created)

		// Upserts do not prune; deletes go by external id or name and may repeat.
		r = api.SyncResult{}
		f.callJSON(t, "POST", url, `{"upserts":[{"name":"c","external_id":"3","address":"10.0.0.3"}],"deletes":[{"external_id":"1"},{"name":"b"},{"external_id":"nope"}]}`, 200, &r)
		assert.Equal(t, []string{"c"}, r.Created)
		assert.ElementsMatch(t, []string{"a", "b"}, r.Deleted)
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 404, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/c", "", 200, nil)

		// Upsert and delete of one node together is refused and writes nothing.
		f.callJSON(t, "POST", url, `{"upserts":[{"name":"c","external_id":"3","address":"10.9.9.9"}],"deletes":[{"external_id":"3"}]}`, 422, nil)
		var n api.Node
		f.callJSON(t, "GET", "/api/v1/nodes/c", "", 200, &n)
		assert.Equal(t, "10.0.0.3", n.Address)
		// A dry run writes nothing.
		f.callJSON(t, "POST", url+"?dry_run=true", `{"deletes":[{"name":"c"}]}`, 200, &r)
		assert.True(t, r.DryRun)
		f.callJSON(t, "GET", "/api/v1/nodes/c", "", 200, nil)
	})
}

// A null list must not read as "no nodes": with prune it would delete the whole
// source. An empty flag must not read as false either.
func TestInventorySyncRefusesNullAndEmptyFlags(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		const url = "/api/v1/inventory/sources/cmdb/nodes"
		f.callJSON(t, "PUT", url, `{"nodes":[{"name":"a","external_id":"1","address":"10.0.0.1"}]}`, 200, nil)
		f.callJSON(t, "PUT", url+"?prune=true", `{"nodes":null}`, 422, nil)
		// In a batch an absent or null list just means none: it never prunes.
		f.callJSON(t, "POST", url+":batch", `{"upserts":null,"deletes":null}`, 200, nil)
		var p problemBody
		f.callJSON(t, "PUT", url+"?dry_run", `{"nodes":[]}`, 422, &p)
		assert.Contains(t, p.locations(), "query.dry_run")
		f.callJSON(t, "PUT", url+"?prune=&dry_run=true", `{"nodes":[]}`, 422, nil)
		f.callJSON(t, "POST", url+":batch?dry_run=", `{}`, 422, nil)
		f.callJSON(t, "GET", "/api/v1/nodes/a", "", 200, nil) // still there
	})
}
