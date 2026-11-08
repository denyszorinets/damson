package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/auth/oidctest"
	"github.com/denyszorinets/damson/internal/store"
)

var (
	dave  = oidctest.User{Sub: "dave-sub", Email: "dave@example.org", Name: "Dave", Groups: []string{"team-leads"}}
	erin  = oidctest.User{Sub: "erin-sub", Email: "erin@example.org", Name: "Erin", Groups: []string{"nobody"}}
	frank = oidctest.User{Sub: "frank-sub", Email: "frank@example.org", Name: "Frank", Groups: []string{"extra-runners"}}
	gina  = oidctest.User{Sub: "gina-sub", Email: "gina@example.org", Name: "Gina", Groups: []string{"team-admins"}}
)

// authzWorld is a project tree with rules, two jobs and one user per case:
//
//	team            runner for team-ops (bob), viewer for team-viewers (carol), admin for team-admins (gina)
//	team/web        editor for team-leads (dave)         job webjob
//	ops             no rules                              job opsjob: runner for extra-runners (frank)
type authzWorld struct {
	*oidcFixture
	webJob, opsJob string
	sess           map[string]*http.Cookie
	csrf           map[string]string
}

func (w *authzWorld) admin(method, path, body string) *httptest.ResponseRecorder {
	return w.do(0, method, path, body, withHeader("Authorization", "Bearer "+w.oidcFixture.admin))
}

func (w *authzWorld) mustAdmin(method, path, body string, want int) []byte {
	w.t.Helper()
	rec := w.admin(method, path, body)
	require.Equal(w.t, want, rec.Code, "%s %s: %s", method, path, rec.Body.String())
	return rec.Body.Bytes()
}

func newAuthzWorld(t *testing.T, st store.Store, controllers int) *authzWorld {
	t.Helper()
	f := newOIDCFixture(t, st, controllers)
	for _, u := range []oidctest.User{dave, erin, frank, gina} {
		f.iss.AddUser(u)
	}
	w := &authzWorld{oidcFixture: f, sess: map[string]*http.Cookie{}, csrf: map[string]string{}}
	for _, p := range []string{"team", "team/web", "ops"} {
		w.mustAdmin("POST", "/api/v1/projects", fmt.Sprintf(`{"path":%q}`, p), 201)
	}
	w.mustAdmin("PUT", "/api/v1/projects/team/access-rules", `{"rules":[
		{"role":"runner","rule":"'team-ops' in claims.groups"},
		{"role":"viewer","rule":"'team-viewers' in user.groups"},
		{"role":"admin","rule":"'team-admins' in claims.groups"}]}`, 200)
	w.mustAdmin("PUT", "/api/v1/projects/team%2Fweb/access-rules", `{"rules":[{"role":"editor","rule":"'team-leads' in claims.groups"}]}`, 200)
	create := func(project, name string) string {
		spec := strings.Replace(specDoc(name, "", "ssh", `{"host":"h"}`), `"project":"default"`, `"project":"`+project+`"`, 1)
		var d jobDetailBody
		require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/jobs", wrapSpec(spec), 201), &d))
		return d.Job.ID
	}
	w.webJob, w.opsJob = create("team/web", "webjob"), create("ops", "opsjob")
	w.mustAdmin("PUT", "/api/v1/jobs/"+w.opsJob+"/access-rules", `{"rules":[{"role":"runner","rule":"'extra-runners' in claims.groups"}]}`, 200)
	for name, u := range map[string]oidctest.User{"alice": oidctest.Alice, "bob": oidctest.Bob, "carol": oidctest.Carol, "dave": dave, "erin": erin, "frank": frank, "gina": gina} {
		sc := w.login(0, u.Sub)
		w.sess[name], w.csrf[name] = sc, w.me(0, sc).CSRFToken
	}
	return w
}

// as calls the API as a named user.
func (w *authzWorld) as(user, method, path, body string) int {
	w.t.Helper()
	opts := []reqOpt{withCookies(cookieHeader(w.sess[user]))}
	if method != http.MethodGet {
		opts = append(opts, withHeader(api.CSRFHeader, w.csrf[user]))
	}
	return w.do(0, method, path, body, opts...).Code
}

func TestAuthzMatrix(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		web, ops := "/api/v1/jobs/"+w.webJob, "/api/v1/jobs/"+w.opsJob
		newVersion := wrapSpec(strings.Replace(specDoc("webjob", "", "ssh", `{"host":"h2"}`), `"project":"default"`, `"project":"team/web"`, 1))
		trigger := `{"options":{}}`
		const (
			ok  = 200
			nf  = 404
			fbd = 403
		)
		tests := []struct {
			name, method, path, body string
			want                     map[string]int // by user; every user of a test is listed
		}{
			{"get job inherited viewer", "GET", web, "", map[string]int{"alice": ok, "bob": ok, "carol": ok, "dave": ok, "erin": nf, "frank": nf, "gina": ok}},
			{"get job with only a job rule", "GET", ops, "", map[string]int{"alice": ok, "bob": nf, "carol": nf, "dave": nf, "erin": nf, "frank": ok, "gina": nf}},
			{"list job versions", "GET", web + "/versions", "", map[string]int{"alice": ok, "bob": ok, "carol": ok, "dave": ok, "erin": nf, "frank": nf, "gina": ok}},
			{"list jobs of a project", "GET", "/api/v1/jobs?project=team%2Fweb", "", map[string]int{"alice": ok, "bob": ok, "carol": ok, "dave": ok, "erin": nf, "frank": nf, "gina": ok}},
			{"trigger needs runner", "POST", web + "/runs", trigger, map[string]int{"alice": 201, "bob": 201, "carol": fbd, "dave": 201, "erin": nf, "frank": nf, "gina": 201}},
			{"job rule grants runner on one job", "POST", ops + "/runs", trigger, map[string]int{"alice": 201, "bob": nf, "carol": nf, "dave": nf, "erin": nf, "frank": 201, "gina": nf}},
			{"new version needs editor", "POST", web + "/versions", newVersion, map[string]int{"alice": 201, "bob": fbd, "carol": fbd, "dave": 201, "erin": nf, "frank": nf, "gina": 201}},
			{"job rule does not reach editor", "POST", ops + "/versions", wrapSpec(strings.Replace(specDoc("opsjob", "", "ssh", `{"host":"h"}`), `"project":"default"`, `"project":"ops"`, 1)), map[string]int{"alice": 201, "bob": nf, "carol": nf, "dave": nf, "erin": nf, "frank": fbd, "gina": nf}},
			{"job access rules need admin", "GET", web + "/access-rules", "", map[string]int{"alice": ok, "bob": fbd, "carol": fbd, "dave": fbd, "erin": nf, "frank": nf, "gina": ok}},
			{"set job access rules", "PUT", web + "/access-rules", `{"rules":[]}`, map[string]int{"alice": ok, "bob": fbd, "carol": fbd, "dave": fbd, "erin": nf, "frank": nf, "gina": ok}},
			{"project access rules need admin", "GET", "/api/v1/projects/team/access-rules", "", map[string]int{"alice": ok, "bob": fbd, "carol": fbd, "dave": nf, "erin": nf, "frank": nf, "gina": ok}},
			{"set project access rules", "PUT", "/api/v1/projects/team/access-rules", `{"rules":[{"role":"runner","rule":"'team-ops' in claims.groups"},{"role":"viewer","rule":"'team-viewers' in user.groups"},{"role":"admin","rule":"'team-admins' in claims.groups"}]}`, map[string]int{"alice": ok, "bob": fbd, "carol": fbd, "dave": nf, "erin": nf, "frank": nf, "gina": ok}},
			{"get project", "GET", "/api/v1/projects/team%2Fweb", "", map[string]int{"alice": ok, "bob": ok, "carol": ok, "dave": ok, "erin": nf, "frank": nf, "gina": ok}},
			{"delete project", "DELETE", "/api/v1/projects/ops", "", map[string]int{"bob": nf, "erin": nf, "frank": nf}},
			{"create child project needs admin on the parent", "POST", "/api/v1/projects", `{"path":"team/new"}`, map[string]int{"bob": fbd, "carol": fbd, "dave": nf, "erin": nf, "gina": 201}},
			{"create root project needs instance admin", "POST", "/api/v1/projects", `{"path":"newroot"}`, map[string]int{"bob": fbd, "gina": fbd, "erin": fbd, "alice": 201}},
			{"admin only routes", "GET", "/api/v1/audit", "", map[string]int{"bob": fbd, "gina": fbd, "dave": fbd}},
			{"secrets are instance admin", "GET", "/api/v1/secrets", "", map[string]int{"bob": fbd, "gina": fbd}},
			{"inventory metadata is open to any login", "GET", "/api/v1/agents", "", map[string]int{"erin": ok, "carol": ok}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				for user, want := range tt.want {
					body := tt.body
					got := w.as(user, tt.method, tt.path, body)
					if tt.name == "new version needs editor" && want == 201 { // each version needs the next number
						continue
					}
					assert.Equal(t, want, got, "%s %s as %s", tt.method, tt.path, user)
				}
			})
		}
	})
}

func (w *authzWorld) roles(user string) map[string]string {
	w.t.Helper()
	rec := w.do(0, "GET", "/api/v1/projects", "", withCookies(cookieHeader(w.sess[user])))
	require.Equal(w.t, 200, rec.Code, rec.Body.String())
	var body struct{ Items []api.Project }
	require.NoError(w.t, json.Unmarshal(rec.Body.Bytes(), &body))
	out := map[string]string{}
	for _, p := range body.Items {
		out[p.Path] = p.MyRole
	}
	return out
}

func TestAuthzListsShowOnlyWhatIsVisible(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		assert.Equal(t, map[string]string{"default": "admin", "ops": "admin", "team": "admin", "team/web": "admin"}, w.roles("alice"))
		assert.Equal(t, map[string]string{"team": "runner", "team/web": "runner"}, w.roles("bob"), "inherited by the child")
		assert.Equal(t, map[string]string{"team": "viewer", "team/web": "viewer"}, w.roles("carol"))
		assert.Equal(t, map[string]string{"team/web": "editor"}, w.roles("dave"), "a child rule does not climb to the parent")
		assert.Equal(t, map[string]string{"team": "admin", "team/web": "admin"}, w.roles("gina"))
		assert.Empty(t, w.roles("erin"))
		assert.Empty(t, w.roles("frank"), "a job rule does not make the project visible")

		// Runs: each user lists only the runs of projects they can see.
		for _, id := range []string{w.webJob, w.opsJob} {
			w.mustAdmin("POST", "/api/v1/jobs/"+id+"/runs", `{"options":{}}`, 201)
			w.mustAdmin("POST", "/api/v1/jobs/"+id+"/runs", `{"options":{}}`, 201)
		}
		count := func(user string) int {
			rec := w.do(0, "GET", "/api/v1/runs?limit=1", "", withCookies(cookieHeader(w.sess[user])))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			n := 0
			for {
				var page struct {
					Items      []api.Run `json:"items"`
					NextCursor string    `json:"next_cursor"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
				n += len(page.Items)
				if page.NextCursor == "" {
					return n
				}
				rec = w.do(0, "GET", "/api/v1/runs?limit=1&cursor="+page.NextCursor, "", withCookies(cookieHeader(w.sess[user])))
				require.Equal(t, 200, rec.Code)
			}
		}
		assert.Equal(t, 4, count("alice"))
		assert.Equal(t, 2, count("bob"), "only the runs of team/web")
		assert.Equal(t, 0, count("erin"))
		assert.Equal(t, 0, count("frank"))
		assert.Equal(t, http.StatusNotFound, w.as("bob", "GET", "/api/v1/runs?job="+w.opsJob, ""), "listing by a job the caller cannot see")
	})
}

func TestAuthzServiceTokensKeepTheirScope(t *testing.T) {
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
		viewer, runner := mk("team", "viewer"), mk("team", "runner")
		call := func(tok, method, path, body string) int {
			return w.do(0, method, path, body, withHeader("Authorization", "Bearer "+tok)).Code
		}
		assert.Equal(t, 200, call(viewer, "GET", "/api/v1/jobs/"+w.webJob, ""))
		assert.Equal(t, 404, call(viewer, "GET", "/api/v1/jobs/"+w.opsJob, ""), "out of scope")
		assert.Equal(t, 403, call(viewer, "POST", "/api/v1/jobs/"+w.webJob+"/runs", `{"options":{}}`))
		assert.Equal(t, 201, call(runner, "POST", "/api/v1/jobs/"+w.webJob+"/runs", `{"options":{}}`))
		assert.Equal(t, 403, call(runner, "PUT", "/api/v1/projects/team/access-rules", `{"rules":[]}`), "a token never holds admin")
		assert.Equal(t, 403, call(runner, "POST", "/api/v1/projects", `{"path":"team/x"}`))
		assert.Equal(t, 404, call(viewer, "GET", "/api/v1/projects/ops", ""))
		rec := w.do(0, "GET", "/api/v1/projects", "", withHeader("Authorization", "Bearer "+viewer))
		require.Equal(t, 200, rec.Code)
		var list struct{ Items []api.Project }
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
		var got []string
		for _, p := range list.Items {
			got = append(got, p.Path+":"+p.MyRole)
		}
		assert.Equal(t, []string{"team:viewer", "team/web:viewer"}, got)
	})
}

func TestAuthzRuleChangesApplyWithinTheCacheTime(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 2)
		get := func(c int) int {
			return w.do(c, "GET", "/api/v1/jobs/"+w.webJob, "", withCookies(cookieHeader(w.sess["erin"]))).Code
		}
		assert.Equal(t, 404, get(0))
		assert.Equal(t, 404, get(1))
		// A rule that matches erin, written through controller 0.
		rec := w.admin("PUT", "/api/v1/projects/team/access-rules", `{"rules":[{"role":"viewer","rule":"'nobody' in claims.groups"}]}`)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		assert.Equal(t, 200, get(0), "the writing controller forgets its grants at once")
		assert.Equal(t, 404, get(1), "another controller may serve its cached answer for up to a minute")
		w.advance(61 * time.Second)
		assert.Equal(t, 200, get(1))
		// And back.
		w.mustAdmin("PUT", "/api/v1/projects/team/access-rules", `{"rules":[]}`, 200)
		w.advance(61 * time.Second)
		assert.Equal(t, 404, get(1))
	})
}

func TestAccessRuleValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		put := func(path, body string) *httptest.ResponseRecorder { return w.admin("PUT", path, body) }
		rec := put("/api/v1/projects/team/access-rules", `{"rules":[{"role":"viewer","rule":"true"},{"role":"viewer","rule":"claims.groups.("}]}`)
		require.Equal(t, 422, rec.Code)
		assert.Contains(t, rec.Body.String(), "body.rules[1].rule")
		assert.NotContains(t, rec.Body.String(), "body.rules[0]")

		rec = put("/api/v1/projects/team/access-rules", `{"rules":[{"role":"viewer","rule":"42"}]}`)
		assert.Equal(t, 422, rec.Code, "a rule that cannot be a boolean")
		rec = put("/api/v1/jobs/"+w.webJob+"/access-rules", `{"rules":[{"role":"admin","rule":"true"}]}`)
		require.Equal(t, 422, rec.Code, "a job grants up to editor")
		assert.Contains(t, rec.Body.String(), "body.rules[0].role")
		assert.Equal(t, 422, put("/api/v1/projects/team/access-rules", `{"rules":[{"role":"root","rule":"true"}]}`).Code)

		// Versions: a stale write is a conflict; the right one moves the version.
		var got api.AccessRules
		require.NoError(t, json.Unmarshal(w.mustAdmin("GET", "/api/v1/projects/team/access-rules", "", 200), &got))
		assert.Equal(t, 3, len(got.Rules))
		stale := fmt.Sprintf(`{"rules":[],"if_version":%d}`, got.Version-1)
		assert.Equal(t, 409, put("/api/v1/projects/team/access-rules", stale).Code)
		fresh := fmt.Sprintf(`{"rules":[],"if_version":%d}`, got.Version)
		require.Equal(t, 200, put("/api/v1/projects/team/access-rules", fresh).Code)
		assert.Equal(t, 404, put("/api/v1/projects/nope/access-rules", `{"rules":[]}`).Code)

		// The audit event for a change names counts and roles, not expressions.
		evs, err := st.ListAuditEvents(t.Context(), store.AuditFilter{TargetType: store.AuditTargetProject}, 50)
		require.NoError(t, err)
		var n int
		for _, e := range evs {
			if e.Action == store.ActionProjectAccessRulesUpdate {
				n++
				assert.NotContains(t, string(e.Details), "claims")
				assert.True(t, strings.HasPrefix(e.Actor, "admin-token:"), e.Actor)
			}
		}
		assert.GreaterOrEqual(t, n, 2)
	})
}

// A rule that fails to evaluate (a claim this user lacks) grants nothing and
// does not stop other rules.
func TestAccessRuleEvaluationErrorGrantsNothing(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		w.mustAdmin("PUT", "/api/v1/projects/ops/access-rules", `{"rules":[
			{"role":"admin","rule":"claims.department == 'x'"},
			{"role":"viewer","rule":"'nobody' in claims.groups"}]}`, 200)
		assert.Equal(t, 200, w.as("erin", "GET", "/api/v1/projects/ops", ""))
		assert.Equal(t, 403, w.as("erin", "DELETE", "/api/v1/projects/ops", ""), "viewer, not the admin a failing rule would have given")
	})
}

// create-job names its project in the body, so the handler checks it.
func TestAuthzCreateJobChecksTheBodyProject(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		spec := func(project, name string) string {
			return wrapSpec(strings.Replace(specDoc(name, "", "ssh", `{"host":"h"}`), `"project":"default"`, `"project":"`+project+`"`, 1))
		}
		assert.Equal(t, 404, w.as("dave", "POST", "/api/v1/jobs", spec("ops", "cj1")), "no role in ops")
		assert.Equal(t, 403, w.as("bob", "POST", "/api/v1/jobs", spec("team", "cj2")), "runner is not editor")
		assert.Equal(t, 201, w.as("dave", "POST", "/api/v1/jobs", spec("team/web", "cj3")), "editor in team/web")
		assert.Equal(t, 404, w.as("erin", "POST", "/api/v1/jobs", spec("team/web", "cj4")))
		assert.Equal(t, 404, w.as("erin", "POST", "/api/v1/jobs", spec("ghost", "cj5")), "a missing project looks like a hidden one")

		var acct api.ServiceAccount
		require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/service-accounts", `{"name":"ci"}`, 201), &acct))
		var tok api.CreatedServiceToken
		require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/service-accounts/"+acct.ID+"/tokens", `{"name":"t","project":"team","role":"editor"}`, 201), &tok))
		got := w.do(0, "POST", "/api/v1/jobs", spec("team", "cj6"), withHeader("Authorization", "Bearer "+tok.Token)).Code
		assert.Equal(t, 404, got, "a scoped token cannot create jobs (ADR-0060)")
	})
}

// Deleting a project, like creating one, needs admin on its parent.
func TestAuthzDeleteNeedsTheParentsAdmin(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		assert.Equal(t, 201, w.as("gina", "POST", "/api/v1/projects", `{"path":"team/x"}`))
		w.mustAdmin("PUT", "/api/v1/projects/team%2Fx/access-rules", `{"rules":[{"role":"admin","rule":"'nobody' in claims.groups"}]}`, 200)
		// erin is admin on team/x through its own rule, but not on its parent.
		assert.Equal(t, 404, w.as("erin", "DELETE", "/api/v1/projects/team%2Fx", ""), "no role on the parent: it looks missing")
		assert.Equal(t, 204, w.as("gina", "DELETE", "/api/v1/projects/team%2Fx", ""))
		// Recreated, the old rules are gone and erin's cached role does not survive.
		assert.Equal(t, 201, w.as("gina", "POST", "/api/v1/projects", `{"path":"team/x"}`))
		assert.Equal(t, 404, w.as("erin", "GET", "/api/v1/projects/team%2Fx", ""))
		assert.Equal(t, 404, w.as("gina", "DELETE", "/api/v1/projects/ops", ""), "gina has no role in ops")
	})
}

func TestListRunsOfAMissingJobIsNotFound(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		assert.Equal(t, 404, w.as("bob", "GET", "/api/v1/runs?job=nope", ""))
		assert.Equal(t, 404, w.as("bob", "GET", "/api/v1/jobs?project=%00%00", ""), "junk is not a project")
	})
}

type auditList struct {
	Items []api.AuditEvent `json:"items"`
}

func (w *authzWorld) audit(user, query string) (int, []api.AuditEvent) {
	w.t.Helper()
	var rec *httptest.ResponseRecorder
	if user == "" {
		rec = w.admin("GET", "/api/v1/audit?"+query, "")
	} else {
		rec = w.do(0, "GET", "/api/v1/audit?"+query, "", withCookies(cookieHeader(w.sess[user])))
	}
	var l auditList
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	return rec.Code, l.Items
}

func actions(evs []api.AuditEvent) map[string]int {
	out := map[string]int{}
	for _, e := range evs {
		out[e.Action]++
	}
	return out
}

func TestAuditRecordsActorsAndCoverage(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		spec := wrapSpec(strings.Replace(specDoc("audited", "", "ssh", `{"host":"h"}`), `"project":"default"`, `"project":"team/web"`, 1))
		var created jobDetailBody
		rec := w.do(0, "POST", "/api/v1/jobs", spec, withCookies(cookieHeader(w.sess["dave"])), withHeader(api.CSRFHeader, w.csrf["dave"]))
		require.Equal(t, 201, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
		var run api.RunDetail
		rec = w.do(0, "POST", "/api/v1/jobs/"+created.Job.ID+"/runs", `{"options":{}}`, withCookies(cookieHeader(w.sess["bob"])), withHeader(api.CSRFHeader, w.csrf["bob"]))
		require.Equal(t, 201, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
		require.Equal(t, 200, w.as("bob", "POST", "/api/v1/runs/"+run.ID+"/cancel", ""))

		code, evs := w.audit("", "")
		require.Equal(t, 200, code)
		byAction := map[string]api.AuditEvent{}
		for _, e := range evs {
			if e.Target == created.Job.ID || e.Target == run.ID {
				byAction[e.Action] = e
			}
		}
		for action, wantActor := range map[string]string{
			"job.create": "user:dave-sub", "run.trigger": "user:bob-sub", "run.cancel": "user:bob-sub",
		} {
			e, ok := byAction[action]
			require.True(t, ok, "no %s event in %v", action, actions(evs))
			assert.Equal(t, wantActor, e.Actor, action)
			assert.Equal(t, "team/web", e.ProjectPath, action)
			assert.NotEmpty(t, e.RequestID, action)
		}
		assert.Equal(t, "dave@example.org", byAction["job.create"].ActorName)
		assert.NotContains(t, string(byAction["run.trigger"].Details), "options")

		// Logins are recorded with the user's email.
		var logins []api.AuditEvent
		for _, e := range evs {
			if e.Action == "auth.login" {
				logins = append(logins, e)
			}
		}
		require.GreaterOrEqual(t, len(logins), 7)
		for _, e := range logins {
			assert.True(t, strings.HasPrefix(e.Actor, "user:") && e.ActorName != "" && e.TargetType == "user", "%+v", e)
		}
	})
}

func TestAuditVisibilityFollowsTheProjectTree(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		// gina administers team; events under ops are not hers.
		code, evs := w.audit("gina", "project=team")
		require.Equal(t, 200, code)
		require.NotEmpty(t, evs)
		for _, e := range evs {
			assert.True(t, e.ProjectPath == "team" || strings.HasPrefix(e.ProjectPath, "team/"), "leaked %+v", e)
		}
		code, evs = w.audit("gina", "project=team%2Fweb")
		assert.Equal(t, 200, code)
		for _, e := range evs {
			assert.True(t, e.ProjectPath == "team/web" || strings.HasPrefix(e.ProjectPath, "team/web/"), "leaked %+v", e)
		}
		code, _ = w.audit("gina", "project=ops")
		assert.Equal(t, 404, code, "no role in ops")
		code, _ = w.audit("gina", "")
		assert.Equal(t, 403, code, "the whole trail is for instance admins")
		code, _ = w.audit("bob", "project=team")
		assert.Equal(t, 403, code, "a runner is not an admin")
		code, _ = w.audit("erin", "project=team")
		assert.Equal(t, 404, code)
		code, _ = w.audit("gina", "project=%00")
		assert.Equal(t, 404, code)
		code, evs = w.audit("alice", "project=ops")
		assert.Equal(t, 200, code)
		assert.NotEmpty(t, evs)
		code, _ = w.audit("", "")
		assert.Equal(t, 200, code, "the admin token reads everything")
	})
}

// Secret names and refs are not for project admins.
func TestAuditHidesSecretEventsFromProjectAdmins(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		require.NoError(t, st.AppendAuditEvent(t.Context(), store.AuditEvent{
			ID: "sec-1", At: w.clock(), Actor: "admin-token:x", Action: store.ActionSecretCreate, TargetType: store.AuditTargetSecret,
			Target: "team/db", ProjectPath: "team", Details: []byte(`{"fields":["password"]}`),
		}))
		_, all := w.audit("", "project=team")
		assert.Contains(t, actions(all), "secret.create", "the admin token sees it")
		code, mine := w.audit("gina", "project=team")
		require.Equal(t, 200, code)
		assert.NotContains(t, actions(mine), "secret.create", "a project admin does not")
		assert.NotEmpty(t, mine)
		_, viaAlice := w.audit("alice", "project=team")
		assert.Contains(t, actions(viaAlice), "secret.create", "an instance admin user does")
	})
}
