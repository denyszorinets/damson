package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

// The users of newAuthzWorld on the web job (project team/web): alice is an
// instance admin, bob a runner and carol a viewer through team, dave an editor
// on team/web, gina an admin on team, erin and frank hold no role there.
// ADR-0076: schedules need editor to write and viewer to read; webhooks need
// admin to create or revoke and viewer to read.

func (w *authzWorld) mkSchedule(job, name string) string {
	w.t.Helper()
	var s api.Schedule
	require.NoError(w.t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/schedules",
		fmt.Sprintf(`{"job_id":%q,"name":%q,"cron":"*/5 * * * *"}`, job, name), 201), &s))
	return s.ID
}

func (w *authzWorld) mkWebhook(job, name string) string {
	w.t.Helper()
	var h api.CreatedWebhook
	require.NoError(w.t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/webhooks",
		fmt.Sprintf(`{"job_id":%q,"name":%q}`, job, name), 201), &h))
	return h.ID
}

func TestScheduleAndWebhookRoutesFollowProjectRoles(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		const (
			ok  = 200
			nf  = 404
			fbd = 403
		)
		users := []string{"alice", "bob", "carol", "dave", "erin", "frank", "gina"}
		type row map[string]int
		all := func(c int) row {
			r := row{}
			for _, u := range users {
				r[u] = c
			}
			return r
		}
		with := func(base row, over row) row {
			r := row{}
			for k, v := range base {
				r[k] = v
			}
			for k, v := range over {
				r[k] = v
			}
			return r
		}
		// Whoever has some role may read; whoever has none gets the missing-resource answer.
		read := with(all(ok), row{"erin": nf, "frank": nf})
		// Schedules are written by editor and up, webhooks by admin only.
		editorWrite := with(read, row{"bob": fbd, "carol": fbd})
		adminWrite := with(read, row{"bob": fbd, "carol": fbd, "dave": fbd})

		sched := w.mkSchedule(w.webJob, "base")
		hook := w.mkWebhook(w.webJob, "base")
		n := 0
		tests := []struct {
			name, method string
			path         func() string
			body         func() string
			ok           int // the status of a call that is allowed
			want         row
		}{
			{"list schedules by job", "GET", func() string { return "/api/v1/schedules?job_id=" + w.webJob }, nil, 200, read},
			{"get schedule", "GET", func() string { return "/api/v1/schedules/" + sched }, nil, 200, read},
			{"create schedule", "POST", func() string { return "/api/v1/schedules" }, func() string {
				n++
				return fmt.Sprintf(`{"job_id":%q,"name":"c%d","cron":"0 * * * *"}`, w.webJob, n)
			}, 201, editorWrite},
			{"patch schedule", "PATCH", func() string { return "/api/v1/schedules/" + sched }, func() string { return `{"enabled":false}` }, 200, editorWrite},
			{"delete schedule", "DELETE", func() string { return "/api/v1/schedules/" + w.mkSchedule(w.webJob, fmt.Sprintf("d%d", n)) }, nil, 204, editorWrite},
			{"list webhooks by job", "GET", func() string { return "/api/v1/webhooks?job_id=" + w.webJob }, nil, 200, read},
			{"get webhook", "GET", func() string { return "/api/v1/webhooks/" + hook }, nil, 200, read},
			{"create webhook", "POST", func() string { return "/api/v1/webhooks" }, func() string {
				n++
				return fmt.Sprintf(`{"job_id":%q,"name":"h%d"}`, w.webJob, n)
			}, 201, adminWrite},
			{"revoke webhook", "DELETE", func() string { return "/api/v1/webhooks/" + w.mkWebhook(w.webJob, fmt.Sprintf("r%d", n)) }, nil, 204, adminWrite},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				for _, user := range users {
					n++
					body := ""
					if tt.body != nil {
						body = tt.body()
					}
					got := w.as(user, tt.method, tt.path(), body)
					want := tt.want[user]
					if want == ok {
						want = tt.ok
					}
					assert.Equal(t, want, got, "%s as %s", tt.name, user)
				}
			})
		}
	})
}

func TestJobRuleEditorManagesThatJobsSchedulesButNotWebhooks(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		// frank holds a job rule on the ops job and no project role: raise it to editor, the most a job rule grants.
		w.mustAdmin("PUT", "/api/v1/jobs/"+w.opsJob+"/access-rules", `{"rules":[{"role":"editor","rule":"'extra-runners' in claims.groups"}]}`, 200)
		sched := fmt.Sprintf(`{"job_id":%q,"name":"j","cron":"0 * * * *"}`, w.opsJob)
		hook := fmt.Sprintf(`{"job_id":%q,"name":"j"}`, w.opsJob)
		assert.Equal(t, 201, w.as("frank", "POST", "/api/v1/schedules", sched))
		assert.Equal(t, 403, w.as("frank", "POST", "/api/v1/webhooks", hook), "a job rule never reaches admin")
		assert.Equal(t, 200, w.as("frank", "GET", "/api/v1/schedules?job_id="+w.opsJob, ""))
		assert.Equal(t, 404, w.as("frank", "GET", "/api/v1/schedules?job_id="+w.webJob, ""), "no role on another job")
		// The job rule does not make the project visible to a project list.
		assert.Equal(t, 404, w.as("frank", "GET", "/api/v1/schedules?project=ops", ""))
		assert.Equal(t, 404, w.as("bob", "POST", "/api/v1/schedules", sched), "bob holds no role on ops")
	})
}

func TestTriggerListsFilterByProjectSubtree(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		w.mkSchedule(w.webJob, "web")
		w.mkSchedule(w.opsJob, "ops")
		w.mkWebhook(w.webJob, "web")
		w.mkWebhook(w.opsJob, "ops")
		count := func(user, path string) (int, int) {
			rec := w.do(0, "GET", path, "", withCookies(cookieHeader(w.sess[user])))
			if rec.Code != http.StatusOK {
				return rec.Code, 0
			}
			var p struct {
				Items []struct {
					JobID string `json:"job_id"`
				} `json:"items"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
			for _, it := range p.Items {
				assert.Equal(t, w.webJob, it.JobID, path+" as "+user)
			}
			return rec.Code, len(p.Items)
		}
		for _, kind := range []string{"schedules", "webhooks"} {
			base := "/api/v1/" + kind
			for _, c := range []struct {
				user, query string
				code, n     int
			}{
				{"bob", "?project=team", 200, 1}, // the web job lies below team
				{"bob", "?project=team%2Fweb", 200, 1},
				{"bob", "?project=ops", 404, 0}, // no role looks like no project
				{"bob", "?project=nope", 404, 0},
				{"bob", "", 400, 0},               // a user must say which job or project
				{"dave", "?project=team", 404, 0}, // a role on a child gives no view of its parent
				{"dave", "?project=team%2Fweb", 200, 1},
				{"erin", "?project=team%2Fweb", 404, 0},
				{"bob", "?project=team&job_id=" + w.webJob, 400, 0},
			} {
				code, n := count(c.user, base+c.query)
				assert.Equal(t, c.code, code, "%s %s%s", c.user, kind, c.query)
				if c.code == 200 {
					assert.Equal(t, c.n, n, "%s %s%s", c.user, kind, c.query)
				}
			}
			// An instance admin may list everything.
			rec := w.admin("GET", base, "")
			require.Equal(t, 200, rec.Code)
			assert.Contains(t, rec.Body.String(), w.opsJob)
			assert.Contains(t, rec.Body.String(), w.webJob)
			// Without credentials the list is refused.
			assert.Equal(t, 401, w.do(0, "GET", base+"?job_id="+w.webJob, "").Code)
		}
	})
}

func TestScheduleAndWebhookServiceTokens(t *testing.T) {
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
		web := fmt.Sprintf(`{"job_id":%q,"name":"%%s","cron":"0 * * * *"}`, w.webJob)
		ops := fmt.Sprintf(`{"job_id":%q,"name":"t","cron":"0 * * * *"}`, w.opsJob)
		assert.Equal(t, 200, call(viewer, "GET", "/api/v1/schedules?job_id="+w.webJob, ""))
		assert.Equal(t, 403, call(viewer, "POST", "/api/v1/schedules", fmt.Sprintf(web, "v")), "the role is checked first")
		assert.Equal(t, 201, call(editor, "POST", "/api/v1/schedules", fmt.Sprintf(web, "e")))
		assert.Equal(t, 404, call(editor, "POST", "/api/v1/schedules", ops), "out of scope")
		assert.Equal(t, 404, call(editor, "GET", "/api/v1/schedules?project=ops", ""), "out of scope")
		assert.Equal(t, 403, call(editor, "POST", "/api/v1/webhooks", fmt.Sprintf(`{"job_id":%q,"name":"x"}`, w.webJob)), "a token never holds admin")
		assert.Equal(t, 200, call(unscoped, "GET", "/api/v1/schedules", ""), "a token that covers every project may list all")
		assert.Equal(t, 404, call(editor, "GET", "/api/v1/schedules", ""), "a scoped token must name its project")
		assert.Equal(t, 201, call(unscoped, "POST", "/api/v1/schedules", ops))
	})
}

func TestWebhookSecretIsShownOnceAndAuditNamesTheProject(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		// gina is an admin on team: she creates a webhook on the web job.
		rec := w.do(0, "POST", "/api/v1/webhooks", fmt.Sprintf(`{"job_id":%q,"name":"gh"}`, w.webJob),
			withCookies(cookieHeader(w.sess["gina"])), withHeader(api.CSRFHeader, w.csrf["gina"]))
		require.Equal(t, 201, rec.Code, rec.Body.String())
		var created api.CreatedWebhook
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
		require.NotEmpty(t, created.Token)

		// No read, list or audit event carries the token or its hash.
		for _, path := range []string{"/api/v1/webhooks/" + created.ID, "/api/v1/webhooks?job_id=" + w.webJob} {
			body := w.do(0, "GET", path, "", withCookies(cookieHeader(w.sess["carol"]))).Body.String()
			assert.NotContains(t, body, created.Token, path)
			assert.NotContains(t, body, "token_hash", path)
		}
		w.mustAdmin("POST", "/api/v1/schedules", fmt.Sprintf(`{"job_id":%q,"name":"s","cron":"0 * * * *"}`, w.webJob), 201)
		w.mustAdmin("DELETE", "/api/v1/webhooks/"+created.ID, "", 204)

		_, evs := w.audit("", "")
		seen := map[string]string{}
		for _, e := range evs {
			if e.TargetType == store.AuditTargetSchedule || e.TargetType == store.AuditTargetWebhook {
				seen[e.Action] = e.ProjectPath
				assert.NotContains(t, string(e.Details), created.Token)
			}
		}
		for _, action := range []string{store.ActionWebhookCreate, store.ActionWebhookRevoke, store.ActionScheduleCreate} {
			assert.Equal(t, "team/web", seen[action], action)
		}
		// A project admin sees the events of their project through the audit list.
		code, mine := w.audit("gina", "")
		if code == http.StatusOK {
			for _, e := range mine {
				assert.True(t, strings.HasPrefix(e.ProjectPath, "team"), e.ProjectPath)
			}
		}
	})
}
