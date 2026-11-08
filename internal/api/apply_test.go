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

func jobDoc(project, name, host string) string {
	return fmt.Sprintf(`{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":%q,"name":%q},`+
		`"spec":{"steps":[{"id":"a","plugin":"ssh","with":{"host":%q}}]}}`, project, name, host)
}

const projectDocJSON = `{"apiVersion":"damson.dev/v1","kind":"Project","description":"Infra","access_rules":[{"role":"runner","rule":"'ops' in claims.groups"}],"node_scope":"node.name != ''"}`

func applyBody(source string, flags string, files ...[2]string) string {
	var fs []string
	for _, f := range files {
		fs = append(fs, fmt.Sprintf(`{"path":%q,"content":%s}`, f[0], f[1]))
	}
	extra := ""
	if flags != "" {
		extra = "," + flags
	}
	return fmt.Sprintf(`{"source":%q%s,"files":[%s]}`, source, extra, strings.Join(fs, ","))
}

func (f *schedFixture) apply(t *testing.T, body string, want int) api.ApplyResult {
	t.Helper()
	var res api.ApplyResult
	f.callJSON(t, http.MethodPost, "/api/v1/apply", body, want, &res)
	return res
}

func actionsOf(r api.ApplyResult) string {
	var out []string
	for _, c := range r.Changes {
		out = append(out, c.Action+":"+c.Project+"/"+c.Name)
	}
	return strings.Join(out, " ")
}

func TestApplyCreatesUpdatesAndRoundTrips(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		files := [][2]string{
			{"infra/project.json", projectDocJSON},
			{"infra/deploy.job.json", jobDoc("infra", "deploy", "h1")},
		}
		// A dry run writes nothing.
		res := f.apply(t, applyBody("files:main", `"dry_run":true`, files...), 200)
		assert.True(t, res.DryRun)
		assert.Equal(t, "create:infra/ create:infra/deploy", actionsOf(res))
		f.callJSON(t, "GET", "/api/v1/projects/infra", "", 404, nil)

		res = f.apply(t, applyBody("files:main", "", files...), 200)
		assert.Equal(t, map[string]int{"create": 2}, res.Summary)
		var p api.Project
		f.callJSON(t, "GET", "/api/v1/projects/infra", "", 200, &p)
		assert.Equal(t, "files:main", p.ManagedBy)
		assert.Equal(t, "Infra", p.Description)

		// The same bundle changes nothing.
		res = f.apply(t, applyBody("files:main", "", files...), 200)
		assert.Equal(t, "unchanged:infra/ unchanged:infra/deploy", actionsOf(res))

		// A changed spec is a new version, a dry run of it is not.
		files[1][1] = jobDoc("infra", "deploy", "h2")
		res = f.apply(t, applyBody("files:main", `"dry_run":true`, files...), 200)
		assert.Equal(t, "unchanged:infra/ update:infra/deploy", actionsOf(res))
		assert.Equal(t, 1, res.Changes[1].FromVersion)
		assert.Equal(t, 2, res.Changes[1].ToVersion)
		var list struct{ Items []api.Job }
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		require.Len(t, list.Items, 1)
		assert.Equal(t, 1, list.Items[0].CurrentVersion)
		assert.Equal(t, "files:main", list.Items[0].ManagedBy)
		f.apply(t, applyBody("files:main", "", files...), 200)
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		assert.Equal(t, 2, list.Items[0].CurrentVersion)

		// Export then apply is a no-op.
		var exp api.ExportBundle
		f.callJSON(t, "GET", "/api/v1/export?project=infra", "", 200, &exp)
		require.Len(t, exp.Files, 2)
		assert.Equal(t, "infra/deploy.job.json", exp.Files[0].Path)
		assert.Equal(t, "infra/project.json", exp.Files[1].Path)
		var round []string
		for _, ef := range exp.Files {
			round = append(round, fmt.Sprintf(`{"path":%q,"content":%s}`, ef.Path, ef.Content))
		}
		res = f.apply(t, fmt.Sprintf(`{"source":"files:main","files":[%s]}`, strings.Join(round, ",")), 200)
		assert.Equal(t, "unchanged:infra/ unchanged:infra/deploy", actionsOf(res))

		// Audit: the events carry the source and the actor.
		evs, err := st.ListAuditEvents(t.Context(), store.AuditFilter{Project: "infra"}, 50)
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, e := range evs {
			seen[e.Action] = true
			if e.Action == store.ActionJobCreate {
				assert.Contains(t, string(e.Details), `"source":"files:main"`)
			}
		}
		for _, a := range []string{store.ActionProjectCreate, store.ActionJobCreate, store.ActionJobVersionCreate, store.ActionProjectAccessRulesUpdate} {
			assert.True(t, seen[a], "missing %s in %v", a, seen)
		}
	})
}

func TestApplyManagedObjectsRefuseOtherWrites(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		f.apply(t, applyBody("files:main", "", [2]string{"infra/project.json", projectDocJSON}, [2]string{"infra/deploy.job.json", jobDoc("infra", "deploy", "h1")}), 200)
		var list struct{ Items []api.Job }
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		id := list.Items[0].ID

		rec := f.callJSON(t, "POST", "/api/v1/jobs/"+id+"/versions", wrapSpec(jobDoc("infra", "deploy", "h9")), 409, nil)
		assert.Contains(t, rec.Body.String(), "managed by files:main")
		rec = f.callJSON(t, "PATCH", "/api/v1/projects/infra", `{"description":"x"}`, 409, nil)
		assert.Contains(t, rec.Body.String(), "managed by files:main")
		f.callJSON(t, "PUT", "/api/v1/projects/infra/access-rules", `{"rules":[]}`, 409, nil)
		f.callJSON(t, "PUT", "/api/v1/projects/infra/node-scope", `{"node_scope":""}`, 409, nil)
		f.callJSON(t, "DELETE", "/api/v1/projects/infra", "", 409, nil)
		// Job access rules stay API-managed.
		f.callJSON(t, "PUT", "/api/v1/jobs/"+id+"/access-rules", `{"rules":[]}`, 200, nil)
	})
}

func TestApplyReportsEveryProblem(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		body := applyBody("files:main", "",
			[2]string{"infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project","node_scope":"this is not CEL ((("}`},
			[2]string{"infra/a.job.json", jobDoc("infra", "different", "h")},
			[2]string{"infra/b.job.json", `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"infra","name":"b"},"spec":{"steps":[]}}`},
			[2]string{"../escape/c.job.json", jobDoc("x", "c", "h")},
		)
		rec := f.callJSON(t, "POST", "/api/v1/apply", body, 422, nil)
		var prob struct {
			Errors []struct{ Message, Location string } `json:"errors"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &prob))
		locs := map[string]bool{}
		for _, e := range prob.Errors {
			locs[strings.SplitN(e.Location, ".content", 2)[0]] = true
		}
		for _, want := range []string{"body.files[0]", "body.files[1]", "body.files[2]", "body.files[3]"} {
			assert.True(t, locs[want], "no problem located at %s: %s", want, rec.Body.String())
		}
		f.callJSON(t, "GET", "/api/v1/projects/infra", "", 404, nil)

		for _, src := range []string{"ui", "git:main", "files:", "other"} {
			f.callJSON(t, "POST", "/api/v1/apply", applyBody(src, ""), 422, nil)
		}
	})
}

func TestApplyAdoptAndOwnership(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st) // sched-job lives in default, managed by ui
		bundle := [][2]string{{"default/sched-job.job.json", jobDoc("default", "sched-job", "other-host")}}
		rec := f.callJSON(t, "POST", "/api/v1/apply", applyBody("files:main", "", bundle...), 422, nil)
		assert.Contains(t, rec.Body.String(), "--adopt")
		res := f.apply(t, applyBody("files:main", `"adopt":true`, bundle...), 200)
		assert.Equal(t, "adopt:default/sched-job", actionsOf(res))
		// Now it belongs to files:main: another source cannot take it, even with adopt.
		rec = f.callJSON(t, "POST", "/api/v1/apply", applyBody("files:other", `"adopt":true`, bundle...), 422, nil)
		assert.Contains(t, rec.Body.String(), "managed by files:main")
	})
}

func TestApplyPruneArchivesAndRestores(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		keep := [2]string{"infra/keep.job.json", jobDoc("infra", "keep", "h")}
		gone := [2]string{"infra/gone.job.json", jobDoc("infra", "gone", "h")}
		proj := [2]string{"infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`}
		f.apply(t, applyBody("files:main", "", proj, keep, gone), 200)
		var list struct{ Items []api.Job }
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		var goneID string
		for _, j := range list.Items {
			if j.Name == "gone" {
				goneID = j.ID
			}
		}

		// A live schedule blocks the prune and nothing changes.
		f.callJSON(t, "POST", "/api/v1/schedules", fmt.Sprintf(`{"job_id":%q,"name":"nightly","cron":"0 3 * * *","timezone":"UTC"}`, goneID), 201, nil)
		rec := f.callJSON(t, "POST", "/api/v1/apply", applyBody("files:main", `"prune":true`, proj, keep), 422, nil)
		assert.Contains(t, rec.Body.String(), "enabled schedules or webhooks")
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		assert.Len(t, list.Items, 2)

		// Without the schedule it is archived: hidden, not runnable, still readable.
		var scheds struct{ Items []api.Schedule }
		f.callJSON(t, "GET", "/api/v1/schedules?job_id="+goneID, "", 200, &scheds)
		for _, s := range scheds.Items {
			f.callJSON(t, "DELETE", "/api/v1/schedules/"+s.ID, "", 204, nil)
		}
		res := f.apply(t, applyBody("files:main", `"prune":true`, proj, keep), 200)
		assert.Equal(t, "unchanged:infra/ unchanged:infra/keep archive:infra/gone", actionsOf(res))
		f.callJSON(t, "GET", "/api/v1/jobs?project=infra", "", 200, &list)
		require.Len(t, list.Items, 1)
		assert.Equal(t, "keep", list.Items[0].Name)
		var got struct{ Job api.Job }
		f.callJSON(t, "GET", "/api/v1/jobs/"+goneID, "", 200, &got)
		assert.True(t, got.Job.Archived)
		rec = f.callJSON(t, "POST", "/api/v1/jobs/"+goneID+"/runs", `{}`, 409, nil)
		assert.Contains(t, rec.Body.String(), "archived")

		// Putting the file back restores it, and a release frees the project.
		res = f.apply(t, applyBody("files:main", "", proj, keep, gone), 200)
		assert.Contains(t, actionsOf(res), "restore:infra/gone")
		f.apply(t, applyBody("files:main", `"prune":true`, [2]string{"default/none.job.json", jobDoc("default", "none", "h")}), 200)
		var p api.Project
		f.callJSON(t, "GET", "/api/v1/projects/infra", "", 200, &p)
		assert.Equal(t, "ui", p.ManagedBy, "a pruned project goes back to the UI")
	})
}

func TestApplyAuthorization(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		f.apply(t, applyBody("files:main", "", [2]string{"infra/project.json", projectDocJSON}, [2]string{"infra/deploy.job.json", jobDoc("infra", "deploy", "h1")}), 200)
		a := f.newAccount(t, "ci")
		editor := f.newToken(t, a.ID, "infra", "editor").Token
		runner := f.newToken(t, a.ID, "infra", "runner").Token
		elsewhere := f.newToken(t, a.ID, "default", "editor").Token

		same := applyBody("files:main", "", [2]string{"infra/project.json", projectDocJSON}, [2]string{"infra/deploy.job.json", jobDoc("infra", "deploy", "h1")})
		changedJob := applyBody("files:main", "", [2]string{"infra/project.json", projectDocJSON}, [2]string{"infra/deploy.job.json", jobDoc("infra", "deploy", "h2")})
		changedProject := applyBody("files:main", "", [2]string{"infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project","description":"new"}`})

		assert.Equal(t, 200, f.as(editor, "POST", "/api/v1/apply", changedJob).Code, "an editor applies job changes")
		assert.Equal(t, 200, f.as(editor, "POST", "/api/v1/apply", same).Code, "and unchanged project files")
		assert.Equal(t, 403, f.as(editor, "POST", "/api/v1/apply", changedProject).Code, "but not project changes")
		assert.Equal(t, 403, f.as(runner, "POST", "/api/v1/apply", same).Code)
		assert.Equal(t, 404, f.as(elsewhere, "POST", "/api/v1/apply", same).Code, "a token scoped elsewhere sees nothing")
		assert.Equal(t, 401, f.as("", "POST", "/api/v1/apply", same).Code)

		// Problems about a project are not shown to a caller without a role there:
		// they would reveal that it exists, who owns it and what it holds.
		other := applyBody("files:other", "", [2]string{"infra/deploy.job.json", jobDoc("infra", "deploy", "h1")})
		assert.Equal(t, 404, f.as(elsewhere, "POST", "/api/v1/apply", other).Code, "ownership of a project the caller cannot see")
		ghost := applyBody("files:x", "", [2]string{"nope/x/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`})
		assert.Equal(t, 404, f.as(elsewhere, "POST", "/api/v1/apply", ghost).Code, "a missing parent of a hidden path")
		assert.Equal(t, 422, f.as(editor, "POST", "/api/v1/apply", other).Code, "a caller with a role is told")

		// Export needs admin: a service token never has it.
		assert.Equal(t, 403, f.as(editor, "GET", "/api/v1/export?project=infra", "").Code)
		assert.Equal(t, 401, f.as("", "GET", "/api/v1/export?project=infra", "").Code)
		f.callJSON(t, "GET", "/api/v1/export?project=nope", "", 404, nil)
	})
}

func TestApplyPruneNeedsFiles(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		f.apply(t, applyBody("files:main", "", [2]string{"infra/project.json", projectDocJSON}), 200)
		rec := f.callJSON(t, "POST", "/api/v1/apply", applyBody("files:main", `"prune":true`), 422, nil)
		assert.Contains(t, rec.Body.String(), "empty bundle")
		var p api.Project
		f.callJSON(t, "GET", "/api/v1/projects/infra", "", 200, &p)
		assert.Equal(t, "files:main", p.ManagedBy, "nothing was released")
	})
}

func TestExportRefusesNamesThatDoNotSurviveAsFiles(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		f.callJSON(t, "POST", "/api/v1/projects", `{"path":"x"}`, 201, nil)
		for _, name := range []string{".hidden", "Deploy", "deploy"} {
			f.callJSON(t, "POST", "/api/v1/jobs", wrapSpec(jobDoc("x", name, "h")), 201, nil)
		}
		rec := f.callJSON(t, "GET", "/api/v1/export?project=x", "", 422, nil)
		assert.Contains(t, rec.Body.String(), `.hidden`)
		assert.Contains(t, rec.Body.String(), `deploy`, "the second of two names that differ only by case")
	})
}

func TestCreateJobWithAnArchivedName(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		proj := [2]string{"infra/project.json", `{"apiVersion":"damson.dev/v1","kind":"Project"}`}
		f.apply(t, applyBody("files:main", "", proj, [2]string{"infra/old.job.json", jobDoc("infra", "old", "h")}), 200)
		f.apply(t, applyBody("files:main", `"prune":true`, proj), 200) // archives old
		rec := f.callJSON(t, "POST", "/api/v1/jobs", wrapSpec(jobDoc("infra", "old", "h")), 409, nil)
		assert.Contains(t, rec.Body.String(), "archived")
	})
}
