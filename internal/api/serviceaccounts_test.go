package api_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
)

func (f *schedFixture) newAccount(t *testing.T, name string) api.ServiceAccount {
	t.Helper()
	var a api.ServiceAccount
	f.callJSON(t, http.MethodPost, "/api/v1/service-accounts", fmt.Sprintf(`{"name":%q}`, name), 201, &a)
	return a
}

func (f *schedFixture) newToken(t *testing.T, account, project, role string) api.CreatedServiceToken {
	t.Helper()
	var tok api.CreatedServiceToken
	f.callJSON(t, http.MethodPost, "/api/v1/service-accounts/"+account+"/tokens",
		fmt.Sprintf(`{"name":"t","project":%q,"role":%q}`, project, role), 201, &tok)
	return tok
}

func TestServiceAccountsRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		pool, _, _ := tokens.New()
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope", api.ServiceTokenPrefix + "nope"} {
			for _, r := range [][3]string{
				{"GET", "/api/v1/service-accounts", ""}, {"POST", "/api/v1/service-accounts", `{"name":"x"}`},
				{"GET", "/api/v1/service-accounts/x", ""}, {"DELETE", "/api/v1/service-accounts/x", ""},
				{"GET", "/api/v1/service-accounts/x/tokens", ""}, {"POST", "/api/v1/service-accounts/x/tokens", `{"name":"t","project":"","role":"viewer"}`},
				{"DELETE", "/api/v1/tokens/x", ""},
			} {
				assert.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], r[2], tok).Code, "%s %s", r[0], r[1])
			}
		}
	})
}

func TestServiceAccountLifecycle(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		a := f.newAccount(t, "ci")
		f.callJSON(t, "POST", "/api/v1/service-accounts", `{"name":"ci"}`, 409, nil)
		f.callJSON(t, "POST", "/api/v1/service-accounts", `{"name":"Bad Name"}`, 422, nil)
		tok := f.newToken(t, a.ID, "default", "runner")
		require.True(t, strings.HasPrefix(tok.Token, api.ServiceTokenPrefix))
		assert.Equal(t, "runner", tok.Role)

		// The secret is shown once.
		list := f.do("GET", "/api/v1/service-accounts/"+a.ID+"/tokens", "", f.admin)
		assert.Equal(t, 200, list.Code)
		assert.NotContains(t, list.Body.String(), tok.Token)
		assert.NotContains(t, list.Body.String(), tokens.Hash(tok.Token))

		// Validation.
		base := "/api/v1/service-accounts/" + a.ID + "/tokens"
		f.callJSON(t, "POST", base, `{"name":"t","project":"nope","role":"viewer"}`, 422, nil)
		f.callJSON(t, "POST", base, `{"name":"t","project":"","role":"admin"}`, 422, nil)
		past := t0.Add(-time.Hour).Format(time.RFC3339)
		f.callJSON(t, "POST", base, fmt.Sprintf(`{"name":"t","project":"","role":"viewer","expires_at":%q}`, past), 422, nil)
		f.callJSON(t, "POST", "/api/v1/service-accounts/nope/tokens", `{"name":"t","project":"","role":"viewer"}`, 404, nil)

		f.callJSON(t, "DELETE", "/api/v1/tokens/"+tok.ID, "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/tokens/"+tok.ID, "", 204, nil)
		f.callJSON(t, "DELETE", "/api/v1/tokens/nope", "", 404, nil)
		f.callJSON(t, "DELETE", "/api/v1/service-accounts/"+a.ID, "", 204, nil)
		f.callJSON(t, "GET", "/api/v1/service-accounts/"+a.ID, "", 404, nil)

		var audit struct {
			Items []api.AuditEvent `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/audit?limit=50", "", 200, &audit)
		var n int
		for _, e := range audit.Items {
			if strings.HasPrefix(e.Action, "service_") {
				n++
				assert.NotContains(t, string(e.Details), tok.Token)
				assert.NotContains(t, string(e.Details), tokens.Hash(tok.Token))
			}
		}
		assert.Equal(t, 4, n, "account create, token create, token revoke (once), account delete")
		assert.NotContains(t, f.logs.String(), tok.Token)
	})
}

func (f *schedFixture) as(token, method, target, body string) *httptest.ResponseRecorder {
	return f.do(method, target, body, token)
}

func TestServiceTokenAuthorization(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		a := f.newAccount(t, "ci")
		viewer := f.newToken(t, a.ID, "default", "viewer").Token
		runner := f.newToken(t, a.ID, "default", "runner").Token
		editorAll := f.newToken(t, a.ID, "", "editor").Token
		jobRuns := "/api/v1/jobs/" + f.jobID + "/runs"

		// Roles.
		assert.Equal(t, 200, f.as(viewer, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		assert.Equal(t, 200, f.as(viewer, "GET", "/api/v1/runs?job="+f.jobID, "").Code)
		assert.Equal(t, 403, f.as(viewer, "POST", jobRuns, `{}`).Code)
		rec := f.as(runner, "POST", jobRuns, `{}`)
		require.Equal(t, 201, rec.Code, rec.Body.String())
		run := f.runOf(t, rec)
		assert.Contains(t, string(run.Trigger), `"actor":"sa:`+a.ID+`"`)
		assert.Equal(t, 200, f.as(runner, "GET", "/api/v1/runs/"+run.ID, "").Code)
		assert.Equal(t, 403, f.as(runner, "POST", "/api/v1/jobs/"+f.jobID+"/versions", `{}`).Code, "runner may not edit")

		// Scope: a project-scoped token cannot act where it covers no project.
		spec := wrapSpec(specDoc("scoped-new", "", "ssh", `{"host":"h"}`))
		editorDefault := f.newToken(t, a.ID, "default", "editor").Token
		assert.Equal(t, 404, f.as(editorDefault, "POST", "/api/v1/jobs", spec).Code, "creating a job needs an unrestricted token")
		assert.Equal(t, 200, f.as(editorDefault, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		assert.Equal(t, 404, f.as(viewer, "GET", "/api/v1/runs", "").Code, "listing every run needs an unrestricted token")
		assert.Equal(t, 201, f.as(editorAll, "POST", "/api/v1/jobs", spec).Code)
		assert.Equal(t, 200, f.as(editorAll, "GET", "/api/v1/runs", "").Code)

		// The admin token keeps working; other kinds of token do not.
		assert.Equal(t, 201, f.as(f.admin, "POST", jobRuns, `{}`).Code)
		pool, _, _ := tokens.New()
		for _, bad := range []string{pool, "garbage", api.ServiceTokenPrefix + "nope", tokens.AdminPrefix + "nope"} {
			assert.Equal(t, 401, f.as(bad, "GET", "/api/v1/jobs/"+f.jobID, "").Code, bad)
		}
		// Without a token the routes stay open, as before M8.
		assert.Equal(t, 200, f.as("", "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		// A service token is not an admin token.
		assert.Equal(t, 401, f.as(editorAll, "GET", "/api/v1/service-accounts", "").Code)
		assert.Equal(t, 401, f.as(editorAll, "GET", "/api/v1/nodes", "").Code)

		// Revoked and expired tokens stop working; so does a deleted account.
		var tokens []api.ServiceToken
		var l struct {
			Items []api.ServiceToken `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/service-accounts/"+a.ID+"/tokens", "", 200, &l)
		tokens = l.Items
		require.Len(t, tokens, 4)
		for _, tk := range tokens {
			f.callJSON(t, "DELETE", "/api/v1/tokens/"+tk.ID, "", 204, nil)
		}
		assert.Equal(t, 401, f.as(viewer, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		b := f.newToken(t, a.ID, "default", "viewer").Token
		assert.Equal(t, 200, f.as(b, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		f.callJSON(t, "DELETE", "/api/v1/service-accounts/"+a.ID, "", 204, nil)
		assert.Equal(t, 401, f.as(b, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
	})
}

func TestServiceTokenExpires(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		a := f.newAccount(t, "ci")
		exp := t0.Add(time.Hour).Format(time.RFC3339)
		var tok api.CreatedServiceToken
		f.callJSON(t, "POST", "/api/v1/service-accounts/"+a.ID+"/tokens",
			fmt.Sprintf(`{"name":"t","project":"","role":"viewer","expires_at":%q}`, exp), 201, &tok)
		assert.Equal(t, 200, f.as(tok.Token, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
		// The fixture clock is fixed at t0; a server whose clock is past the expiry refuses it.
		cfg := f.cfg
		cfg.Now = func() time.Time { return t0.Add(2 * time.Hour) }
		late := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+f.jobID, nil)
		req.Header.Set("Authorization", "Bearer "+tok.Token)
		api.NewServer(cfg).Handler().ServeHTTP(late, req)
		assert.Equal(t, 401, late.Code)
	})
}

func TestRequireToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		a := f.newAccount(t, "ci")
		tok := f.newToken(t, a.ID, "", "viewer").Token
		cfg := f.cfg
		cfg.RequireToken = true
		h := api.NewServer(cfg).Handler()
		get := func(path, token string) int {
			req := httptest.NewRequest("GET", path, nil)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec.Code
		}
		assert.Equal(t, 401, get("/api/v1/jobs/"+f.jobID, ""))
		assert.Equal(t, 200, get("/api/v1/jobs/"+f.jobID, tok))
		assert.Equal(t, 200, get("/api/v1/jobs/"+f.jobID, f.admin))
		assert.Equal(t, 200, get("/healthz", ""))
	})
}

func (f *schedFixture) runOf(t *testing.T, rec *httptest.ResponseRecorder) runBody {
	t.Helper()
	var r runBody
	require.NoError(t, jsonUnmarshal(rec.Body.Bytes(), &r))
	return r
}
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// insertProject adds a project row, which no API creates until M8.
func insertProject(t *testing.T, st store.Store, id, name string) {
	t.Helper()
	err := st.WithTx(t.Context(), func(tx store.Tx) error {
		switch n := tx.Native().(type) {
		case *sql.Tx:
			_, err := n.ExecContext(t.Context(), "INSERT INTO projects (id, name, created_at) VALUES (?, ?, ?)", id, name, "2026-01-02T03:04:05.000000Z")
			return err
		case pgx.Tx:
			_, err := n.Exec(t.Context(), "INSERT INTO projects (id, name, created_at) VALUES ($1, $2, now())", id, name)
			return err
		}
		return fmt.Errorf("unknown transaction type %T", tx.Native())
	})
	require.NoError(t, err)
}

// A token scoped to one project gets 404 on every governed route of another,
// exactly as for a target that does not exist, and 200 in its own.
func TestServiceTokenProjectScope(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		insertProject(t, st, "00000000-0000-7000-8000-0000000000aa", "other")
		oj, _, err := st.CreateJob(t.Context(), store.NewJob{
			ProjectID: "00000000-0000-7000-8000-0000000000aa", Name: "other-job",
			Spec: json.RawMessage(specDoc("other-job", "", "ssh", `{"host":"h"}`)), Checksum: "x",
		}, t0)
		require.NoError(t, err)

		// A run (with an execution) in each project, started by the admin token.
		runIn := func(jobID string) (runID, execID string) {
			rec := f.as(f.admin, "POST", "/api/v1/jobs/"+jobID+"/runs", `{}`)
			require.Equal(t, 201, rec.Code, rec.Body.String())
			r := f.runOf(t, rec)
			require.NotEmpty(t, r.Steps)
			require.NotEmpty(t, r.Steps[0].Executions)
			return r.ID, r.Steps[0].Executions[0].ID
		}
		otherRun, otherExec := runIn(oj.ID)
		ownRun, ownExec := runIn(f.jobID)

		a := f.newAccount(t, "ci")
		inDefault := f.newToken(t, a.ID, "default", "runner").Token
		inOther := f.newToken(t, a.ID, "other", "runner").Token

		routes := func(job, run, exec, project string) [][2]string {
			return [][2]string{
				{"GET", "/api/v1/jobs/" + job}, {"GET", "/api/v1/jobs/" + job + "/versions"},
				{"GET", "/api/v1/jobs/" + job + "/versions/1/graph"}, {"GET", "/api/v1/jobs/" + job + "/versions/1"},
				{"POST", "/api/v1/jobs/" + job + "/runs"},
				{"GET", "/api/v1/runs/" + run}, {"GET", "/api/v1/runs/" + run + "/graph"}, {"POST", "/api/v1/runs/" + run + "/cancel"},
				{"GET", "/api/v1/executions/" + exec},
				{"GET", "/api/v1/jobs?project=" + project}, {"GET", "/api/v1/runs?job=" + job},
			}
		}
		// Out of scope: 404 everywhere, and no run is created.
		before, err := st.QueryRuns(t.Context(), store.RunFilter{}, "", 50)
		require.NoError(t, err)
		for _, r := range routes(oj.ID, otherRun, otherExec, "other") {
			body := ""
			if r[0] == "POST" {
				body = `{}`
			}
			rec := f.as(inDefault, r[0], r[1], body)
			assert.Equal(t, 404, rec.Code, "%s %s", r[0], r[1])
		}
		after, err := st.QueryRuns(t.Context(), store.RunFilter{}, "", 50)
		require.NoError(t, err)
		assert.Len(t, after.Runs, len(before.Runs), "a refused trigger made no run")
		// The refusal looks like a missing target.
		missing := f.as(inDefault, "GET", "/api/v1/jobs/"+oj.ID, "")
		gone := f.as(inDefault, "GET", "/api/v1/jobs/does-not-exist", "")
		assert.Equal(t, stripID(gone.Body.String()), stripID(missing.Body.String()))

		// In scope: not 404 (cancel of a pending run and re-trigger may answer 200/201/409).
		for _, r := range routes(f.jobID, ownRun, ownExec, "default") {
			body := ""
			if r[0] == "POST" {
				body = `{}`
			}
			assert.NotEqual(t, 404, f.as(inDefault, r[0], r[1], body).Code, "%s %s", r[0], r[1])
		}
		for _, r := range routes(oj.ID, otherRun, otherExec, "other") {
			body := ""
			if r[0] == "POST" {
				body = `{}`
			}
			assert.NotEqual(t, 404, f.as(inOther, r[0], r[1], body).Code, "%s %s", r[0], r[1])
		}
		assert.Equal(t, 404, f.as(inOther, "GET", "/api/v1/jobs/"+f.jobID, "").Code)
	})
}

func TestBearerHeaderIsNotSilentlyIgnored(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		for name, h := range map[string]string{
			"empty bearer": "Bearer ", "bare service token": api.ServiceTokenPrefix + "abc", "bare admin token": tokens.AdminPrefix + "abc",
		} {
			req := httptest.NewRequest("GET", "/api/v1/jobs/"+f.jobID, nil)
			req.Header.Set("Authorization", h)
			rec := httptest.NewRecorder()
			api.NewServer(f.cfg).Handler().ServeHTTP(rec, req)
			assert.Equal(t, 401, rec.Code, name)
		}
		// Other schemes (a proxy's basic auth) are not ours to refuse.
		req := httptest.NewRequest("GET", "/api/v1/jobs/"+f.jobID, nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwdw==")
		rec := httptest.NewRecorder()
		api.NewServer(f.cfg).Handler().ServeHTTP(rec, req)
		assert.Equal(t, 200, rec.Code)
	})
}
