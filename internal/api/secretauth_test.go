package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

// ADR-0077: a secret scope is a project path; editor lists, admin writes and
// deletes; the role is the caller's role on the scope's project. In the authz
// world, bob is a runner and carol a viewer on team, gina an admin on team,
// dave an editor on team/web, erin and frank hold no role there.

const canary = "canary-VALUE-9f3a"

func putBody(typ string) string {
	return fmt.Sprintf(`{"type":%q,"fields":{"token":%q}}`, typ, canary)
}

func (w *authzWorld) secretAs(user, method, scope, name, body string) *httptest.ResponseRecorder {
	w.t.Helper()
	target := "/api/v1/secrets/" + url.PathEscape(scope) + "/" + name
	if user == "" {
		return w.admin(method, target, body)
	}
	opts := []reqOpt{withCookies(cookieHeader(w.sess[user]))}
	if method != http.MethodGet {
		opts = append(opts, withHeader(api.CSRFHeader, w.csrf[user]))
	}
	return w.do(0, method, target, body, opts...)
}

func (w *authzWorld) listSecretsAs(user, query string) (int, []string) {
	w.t.Helper()
	var rec *httptest.ResponseRecorder
	if user == "" {
		rec = w.admin("GET", "/api/v1/secrets"+query, "")
	} else {
		rec = w.do(0, "GET", "/api/v1/secrets"+query, "", withCookies(cookieHeader(w.sess[user])))
	}
	var l struct {
		Items []struct {
			Ref string `json:"ref"`
		} `json:"items"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(rec.Body.Bytes(), &l))
	}
	var refs []string
	for _, it := range l.Items {
		refs = append(refs, it.Ref)
	}
	return rec.Code, refs
}

func TestSecretRoutesFollowTheRolesOfTheirScope(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		put := func(user, scope, name string) int { return w.secretAs(user, "PUT", scope, name, putBody("token")).Code }
		del := func(user, scope, name string) int { return w.secretAs(user, "DELETE", scope, name, "").Code }

		// gina administers team, so also team/web below it.
		assert.Equal(t, 201, put("gina", "team", "db"))
		assert.Equal(t, 200, put("gina", "team", "db"), "a second write adds a version")
		assert.Equal(t, 201, put("gina", "team/web", "api"), "an admin of a parent manages the child's secrets")
		assert.Equal(t, 404, put("gina", "ops", "x"), "no role on ops")
		assert.Equal(t, 404, put("gina", "_inventory", "x"), "the inventory scope is instance admin only")
		assert.Equal(t, 404, put("gina", "team/nope", "x"), "a project that does not exist")
		assert.Equal(t, 201, put("alice", "ops", "x"), "an instance admin writes anywhere")
		assert.Equal(t, 201, put("alice", "team/nope", "orphan"), "and keeps the scope that has no project")

		// dave is an editor on team/web: he lists it and its ancestors' names, writes nothing.
		assert.Equal(t, 403, put("dave", "team/web", "x"))
		assert.Equal(t, 404, put("dave", "team", "x"), "no role on the parent")
		assert.Equal(t, 403, del("dave", "team/web", "api"))
		code, refs := w.listSecretsAs("dave", "?scope=team%2Fweb")
		assert.Equal(t, 200, code)
		assert.Equal(t, []string{"secret://team/web/api"}, refs)
		code, refs = w.listSecretsAs("dave", "?project=team%2Fweb")
		assert.Equal(t, 200, code)
		assert.Equal(t, []string{"secret://team/db", "secret://team/web/api"}, refs, "own scope and the ancestor's, never _inventory")
		code, _ = w.listSecretsAs("dave", "?scope=team")
		assert.Equal(t, 404, code, "a child editor has no view of the parent scope itself")

		// viewer and runner may not even list; erin has no role.
		for _, u := range []string{"bob", "carol"} {
			code, _ = w.listSecretsAs(u, "?scope=team")
			assert.Equal(t, 403, code, u)
			assert.Equal(t, 403, put(u, "team", "x"), u)
			assert.Equal(t, 403, del(u, "team", "db"), u)
		}
		code, _ = w.listSecretsAs("erin", "?scope=team")
		assert.Equal(t, 404, code)
		assert.Equal(t, 404, put("erin", "team", "x"))
		assert.Equal(t, 404, del("erin", "team", "db"))
		// A job rule never counts.
		assert.Equal(t, 404, put("frank", "ops", "x"))
		code, _ = w.listSecretsAs("frank", "?project=ops")
		assert.Equal(t, 404, code)

		// A user must say what to list; an instance admin may list everything.
		code, _ = w.listSecretsAs("gina", "")
		assert.Equal(t, 400, code)
		code, _ = w.listSecretsAs("gina", "?scope=team&project=team")
		assert.Equal(t, 400, code)
		code, refs = w.listSecretsAs("", "")
		assert.Equal(t, 200, code)
		assert.Contains(t, refs, "secret://ops/x")
		assert.Contains(t, refs, "secret://team/nope/orphan")
		// gina's project listing of team never reaches ops or the orphan.
		code, refs = w.listSecretsAs("gina", "?project=team")
		assert.Equal(t, 200, code)
		assert.Equal(t, []string{"secret://team/db"}, refs)

		assert.Equal(t, 204, del("gina", "team/web", "api"))
		assert.Equal(t, 404, del("gina", "team/web", "api"), "already gone")
	})
}

func TestSecretEncodedScopeCannotSlipPastTheCheck(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		// erin holds no role anywhere: the encoded scope must not create or list anything.
		rec := w.do(0, "PUT", "/api/v1/secrets/team%2Fweb/sneak", putBody("token"),
			withCookies(cookieHeader(w.sess["erin"])), withHeader(api.CSRFHeader, w.csrf["erin"]))
		assert.Equal(t, 404, rec.Code)
		code, refs := w.listSecretsAs("", "?scope=team%2Fweb")
		assert.Equal(t, 200, code)
		assert.Empty(t, refs, "nothing was created")
	})
}

func TestSecretServiceTokens(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		var acct api.ServiceAccount
		require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/service-accounts", `{"name":"ci"}`, 201), &acct))
		mk := func(project, role string) string {
			var tok api.CreatedServiceToken
			require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/service-accounts/"+acct.ID+"/tokens",
				fmt.Sprintf(`{"name":"t","project":%q,"role":%q}`, project, role), 201), &tok))
			return tok.Token
		}
		viewer, editor, unscoped := mk("team", "viewer"), mk("team", "editor"), mk("", "editor")
		call := func(tok, method, path, body string) int {
			return w.do(0, method, path, body, withHeader("Authorization", "Bearer "+tok)).Code
		}
		w.mustAdmin("PUT", "/api/v1/secrets/team/db", putBody("token"), 201)
		w.mustAdmin("PUT", "/api/v1/secrets/ops/db", putBody("token"), 201)
		assert.Equal(t, 403, call(viewer, "GET", "/api/v1/secrets?scope=team", ""))
		assert.Equal(t, 200, call(editor, "GET", "/api/v1/secrets?scope=team", ""))
		assert.Equal(t, 200, call(editor, "GET", "/api/v1/secrets?project=team%2Fweb", ""), "ancestor names too")
		assert.Equal(t, 404, call(editor, "GET", "/api/v1/secrets?scope=ops", ""), "out of scope")
		assert.Equal(t, 403, call(editor, "PUT", "/api/v1/secrets/team/x", putBody("token")), "a token never holds admin")
		assert.Equal(t, 403, call(editor, "DELETE", "/api/v1/secrets/team/db", ""))
		assert.Equal(t, 400, call(unscoped, "GET", "/api/v1/secrets", ""), "an unscoped token must still name a scope")
		assert.Equal(t, 200, call(unscoped, "GET", "/api/v1/secrets?scope=ops", ""))
		assert.Equal(t, 401, w.do(0, "GET", "/api/v1/secrets?scope=team", "").Code, "anonymous")
	})
}

func TestSecretValueNeverAppearsInResponsesAuditOrLogs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		var bodies []string
		for _, r := range []*httptest.ResponseRecorder{
			w.secretAs("gina", "PUT", "team", "db", putBody("token")),  // allowed
			w.secretAs("gina", "PUT", "ops", "db", putBody("token")),   // 404
			w.secretAs("carol", "PUT", "team", "db", putBody("token")), // 403
			w.secretAs("erin", "PUT", "team", "db", putBody("token")),  // 404
			w.secretAs("gina", "PUT", "team", "db", `{"type":"nope"}`), // 422, no fields
			w.secretAs("gina", "DELETE", "team", "missing", ""),        // 404
			w.admin("GET", "/api/v1/secrets?scope=team", ""),
		} {
			bodies = append(bodies, r.Body.String())
		}
		_, evs := w.audit("", "")
		details := ""
		var gotPath string
		for _, e := range evs {
			details += string(e.Details) + e.Target
			if strings.HasPrefix(e.Action, "secret.") && e.Target != "" && e.ProjectPath == "team" {
				gotPath = e.ProjectPath
			}
		}
		assert.Equal(t, "team", gotPath, "the audit row of a project admin's write names the project")
		for _, s := range append(bodies, details, w.logs.String()) {
			assert.NotContains(t, s, canary)
		}
	})
}
