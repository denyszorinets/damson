package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

func TestSecretWritesAreAudited(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, true)
		fields := `"fields":{"password":"` + secretValue + `","username":"root"}`
		put := f.do("PUT", "/api/v1/secrets/proj/db", `{"type":"password",`+fields+`}`, f.admin)
		require.Equal(t, http.StatusCreated, put.Code, put.Body.String())
		reqID := put.Header().Get("X-Request-Id")
		require.NotEmpty(t, reqID)
		require.Equal(t, http.StatusOK, f.do("PUT", "/api/v1/secrets/proj/db", `{"type":"password",`+fields+`}`, f.admin).Code)
		require.Equal(t, http.StatusNoContent, f.do("DELETE", "/api/v1/secrets/proj/db", "", f.admin).Code)

		// Rejected or unauthorized writes leave no event.
		assert.Equal(t, http.StatusUnauthorized, f.do("PUT", "/api/v1/secrets/proj/x", `{"type":"generic","fields":{"k":"v"}}`, "").Code)
		assert.Equal(t, http.StatusUnprocessableEntity, f.do("PUT", "/api/v1/secrets/proj/y", `{"type":"nope","fields":{"k":"v"}}`, f.admin).Code)

		var page struct {
			Items []api.AuditEvent `json:"items"`
			Next  string           `json:"next_cursor"`
		}
		rec := f.do("GET", "/api/v1/audit?target=proj%2Fdb", "", f.admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		f.noValue(t, rec)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 3)
		for i, want := range []string{"secret.delete", "secret.update", "secret.create"} {
			e := page.Items[i]
			assert.Equal(t, want, e.Action)
			assert.Regexp(t, `^admin-token:[0-9a-f]{12}$`, e.Actor)
			assert.Equal(t, "proj/db", e.Target)
		}
		assert.Equal(t, reqID, page.Items[2].RequestID, "the create event carries the request id of the PUT")

		// Paging over equal-time events (the fixture clock is fixed).
		rec = f.do("GET", "/api/v1/audit?limit=1", "", f.admin)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		require.NotEmpty(t, page.Next)
		seen := page.Items[0].ID
		rec = f.do("GET", "/api/v1/audit?limit=5&cursor="+page.Next, "", f.admin)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 2)
		assert.NotEqual(t, seen, page.Items[0].ID)

		assert.Equal(t, http.StatusUnauthorized, f.do("GET", "/api/v1/audit", "", "").Code)
		assert.Equal(t, http.StatusBadRequest, f.do("GET", "/api/v1/audit?cursor=!!", "", f.admin).Code)
	})
}

func TestPurgeExecutionLogs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		jr := newJR(t, st, nil)
		sf := newSecretsFixture(t, st, false)
		run, e := jr.startedRun(t)
		jr.putChunk(t, e, 1, 2, "one", "leaked-line")
		target := "/api/v1/executions/" + e.ID + "/logs"

		assert.Equal(t, http.StatusUnauthorized, sf.do("DELETE", target, "", "").Code)
		assert.Equal(t, http.StatusConflict, sf.do("DELETE", target, "", sf.admin).Code, "the execution is still running")
		assert.Equal(t, http.StatusNotFound, sf.do("DELETE", "/api/v1/executions/nope/logs", "", sf.admin).Code)

		jr.finish(t, run, e, store.ExecSucceeded)
		rec := sf.do("DELETE", target, "", sf.admin)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out struct{ Chunks, Objects int64 }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.EqualValues(t, 1, out.Chunks)
		assert.EqualValues(t, 1, out.Objects)

		chunks, err := st.ListLogChunks(t.Context(), e.ID, 0, 0, 10)
		require.NoError(t, err)
		assert.Empty(t, chunks)

		var page struct {
			Items []api.AuditEvent `json:"items"`
		}
		rec = sf.do("GET", "/api/v1/audit?target_type=execution&target="+e.ID, "", sf.admin)
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		assert.Equal(t, "log.purge", page.Items[0].Action)
		assert.Regexp(t, `^admin-token:`, page.Items[0].Actor)
		assert.NotContains(t, rec.Body.String(), "leaked-line")
	})
}

func TestAuditFilters(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSecretsFixture(t, st, false)
		base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
		for i, e := range []struct{ actor, name, action string }{
			{"user:u1", "u1@example.org", "job.create"},
			{"user:u2", "u2@example.org", "job.create"},
			{"system:dispatch", "", "secret.read"},
			{"user:u1", "u1@example.org", "run.trigger"},
		} {
			require.NoError(t, st.AppendAuditEvent(t.Context(), store.AuditEvent{
				ID: fmt.Sprintf("flt-%d", i), At: base.Add(time.Duration(i) * time.Hour), Actor: e.actor, ActorName: e.name,
				Action: e.action, TargetType: "job", Target: fmt.Sprintf("flt-%d", i),
			}))
		}
		list := func(query string) []api.AuditEvent {
			rec := f.do("GET", "/api/v1/audit?target_type=job&"+query, "", f.admin)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var page struct {
				Items []api.AuditEvent `json:"items"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
			return page.Items
		}
		targets := func(query string) []string {
			var out []string
			for _, e := range list(query) {
				out = append(out, e.Target)
			}
			return out
		}
		assert.Equal(t, []string{"flt-3", "flt-0"}, targets("actor=user:u1"))
		assert.Equal(t, "u1@example.org", list("actor=user:u1")[0].ActorName)
		assert.Equal(t, []string{"flt-1", "flt-0"}, targets("action=job.create"))
		assert.Equal(t, []string{"flt-3", "flt-2", "flt-1", "flt-0"}, targets(""), "system rows are listed unless hidden")
		assert.Equal(t, []string{"flt-3", "flt-1", "flt-0"}, targets("hide_system=true"))
		assert.Equal(t, []string{"flt-2", "flt-1"}, targets("from=2026-03-01T11:00:00Z&to=2026-03-01T13:00:00Z"))
		assert.Equal(t, []string{"flt-1"}, targets("from=2026-03-01T11:00:00Z&to=2026-03-01T12:00:00Z"), "to is exclusive")
		for _, bad := range []string{"from=yesterday", "to=2026-03-01"} {
			assert.Equal(t, http.StatusBadRequest, f.do("GET", "/api/v1/audit?"+bad, "", f.admin).Code, bad)
		}
	})
}
