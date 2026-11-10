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

func TestProjectsRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		pool, _, _ := tokens.New()
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			for _, r := range [][3]string{
				{"GET", "/api/v1/projects", ""}, {"POST", "/api/v1/projects", `{"path":"x"}`},
				{"GET", "/api/v1/projects/default", ""}, {"PATCH", "/api/v1/projects/default", `{}`},
				{"DELETE", "/api/v1/projects/default", ""},
			} {
				assert.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], r[2], tok).Code, "%s %s", r[0], r[1])
			}
		}
	})
}

func TestProjectTreeLifecycle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		var root, child api.Project
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"infra","description":"shared"}`, 201, &root)
		assert.Equal(t, "infra", root.Path)
		assert.Empty(t, root.ParentPath)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"infra/web"}`, 201, &child)
		assert.Equal(t, "infra", child.ParentPath)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"infra"}`, 409, nil)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"ghost/web"}`, 422, nil)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"_inventory"}`, 422, nil)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"Infra"}`, 422, nil)

		var list struct{ Items []api.Project }
		f.callJSON(t, "GET", "/api/v1/projects", "", 200, &list)
		require.Len(t, list.Items, 3)
		assert.Equal(t, "default", list.Items[0].Path)

		var got api.Project
		f.callJSON(t, "GET", "/api/v1/projects/infra%2Fweb", "", 200, &got)
		assert.Equal(t, child.ID, got.ID)
		f.callJSON(t, "GET", "/api/v1/projects/nope", "", 404, nil)

		var up api.Project
		f.callJSON(t, "PATCH", "/api/v1/projects/infra%2Fweb", `{"description":"web","if_version":1}`, 200, &up)
		assert.Equal(t, 2, up.Version)
		assert.Equal(t, "web", up.Description)
		f.callJSON(t, "PATCH", "/api/v1/projects/infra%2Fweb", `{"description":"late","if_version":1}`, 409, nil)

		rec := f.callJSON(t, "DELETE", "/api/v1/projects/infra", "", 409, nil)
		assert.Contains(t, rec.Body.String(), "child projects")
		f.callJSON(t, "DELETE", "/api/v1/projects/infra%2Fweb", "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/projects/infra", "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/projects/infra", "", 404, nil)

		evs, err := st.ListAuditEvents(t.Context(), store.AuditFilter{TargetType: store.AuditTargetProject}, 20)
		require.NoError(t, err)
		assert.Len(t, evs, 5) // 2 creates, 1 update, 2 deletes; rejected writes leave no event
	})
}
