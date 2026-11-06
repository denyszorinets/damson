package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

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
