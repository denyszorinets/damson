package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

type treeBody struct {
	Projects       []api.TreeProject `json:"projects"`
	Jobs           []api.TreeJob     `json:"jobs"`
	NextCursor     string            `json:"next_cursor"`
	JobsNextCursor string            `json:"jobs_next_cursor"`
}

func (w *authzWorld) tree(user, query string) (int, treeBody) {
	w.t.Helper()
	rec := w.do(0, "GET", "/api/v1/tree"+query, "", withCookies(cookieHeader(w.sess[user])))
	var b treeBody
	if rec.Code == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(rec.Body.Bytes(), &b))
	}
	return rec.Code, b
}

func labels(ps []api.TreeProject) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Label+":"+p.MyRole)
	}
	return out
}

func TestTreeShowsOnlyWhatIsVisible(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)

		_, b := w.tree("alice", "")
		assert.Equal(t, []string{"default:admin", "ops:admin", "team:admin"}, labels(b.Projects), "an admin sees every root")
		assert.True(t, b.Projects[2].HasChildren)
		assert.False(t, b.Projects[0].HasChildren)

		_, b = w.tree("bob", "")
		assert.Equal(t, []string{"team:runner"}, labels(b.Projects))
		code, b := w.tree("bob", "?parent=team")
		require.Equal(t, 200, code)
		assert.Equal(t, []string{"web:runner"}, labels(b.Projects), "a child shows its last segment")
		assert.Empty(t, b.Jobs)
		_, b = w.tree("bob", "?parent=team%2Fweb")
		require.Len(t, b.Jobs, 1)
		assert.Equal(t, "webjob", b.Jobs[0].Name)
		assert.Equal(t, "runner", b.Jobs[0].MyRole)

		// A role on a child alone gives a root labeled with its full path, no stub of the parent.
		_, b = w.tree("dave", "")
		assert.Equal(t, []string{"team/web:editor"}, labels(b.Projects))
		assert.False(t, b.Projects[0].HasChildren)
		assert.Equal(t, http.StatusNotFound, func() int { c, _ := w.tree("dave", "?parent=team"); return c }(), "the parent is not visible")

		// No role and a missing project answer the same.
		for _, user := range []string{"erin", "frank"} {
			_, b = w.tree(user, "")
			assert.Empty(t, b.Projects, user)
			c1, _ := w.tree(user, "?parent=team")
			c2, _ := w.tree(user, "?parent=does-not-exist")
			assert.Equal(t, http.StatusNotFound, c1, user)
			assert.Equal(t, c1, c2, user)
		}
	})
}

func TestTreePagesProjectsAndJobs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		for _, p := range []string{"team/a", "team/b"} {
			w.mustAdmin("POST", "/api/v1/projects", `{"path":"`+p+`"}`, 201)
		}
		_, page := w.tree("alice", "?parent=team&limit=2")
		assert.Equal(t, []string{"a:admin", "b:admin"}, labels(page.Projects))
		require.NotEmpty(t, page.NextCursor)
		_, page = w.tree("alice", "?parent=team&limit=2&cursor="+page.NextCursor)
		assert.Equal(t, []string{"web:admin"}, labels(page.Projects))
		assert.Empty(t, page.NextCursor)
		code, _ := w.tree("alice", "?cursor=!!")
		assert.Equal(t, http.StatusBadRequest, code)
	})
}

func TestSharedJobsAreOnlyThoseReachedByJobRules(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		shared := func(user string) []api.SharedJob {
			rec := w.do(0, "GET", "/api/v1/tree/shared", "", withCookies(cookieHeader(w.sess[user])))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			var b struct {
				Items []api.SharedJob `json:"items"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
			return b.Items
		}
		got := shared("frank")
		require.Len(t, got, 1)
		assert.Equal(t, api.SharedJob{ID: w.opsJob, Name: "opsjob", MyRole: "runner"}, got[0])
		for _, user := range []string{"alice", "bob", "carol", "dave", "erin", "gina"} {
			assert.Empty(t, shared(user), user)
		}
		// frank has no project node and no tree.
		_, b := w.tree("frank", "")
		assert.Empty(t, b.Projects)
		// A job whose project is visible is in the tree, not in "shared with me".
		w.mustAdmin("PUT", "/api/v1/jobs/"+w.webJob+"/access-rules", `{"rules":[{"role":"viewer","rule":"'extra-runners' in claims.groups"}]}`, 200)
		assert.Len(t, shared("frank"), 2, "a job rule on a hidden project's job")
		w.mustAdmin("PUT", "/api/v1/jobs/"+w.webJob+"/access-rules", `{"rules":[]}`, 200)
		assert.Len(t, shared("frank"), 1)
	})
}

func TestTreeServiceTokenAndAnonymous(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		rec := w.do(0, "GET", "/api/v1/tree", "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		rec = w.do(0, "GET", "/api/v1/tree/shared", "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func (w *authzWorld) scoped(user, query string) (int, []api.Job, string) {
	w.t.Helper()
	rec := w.do(0, "GET", "/api/v1/tree/jobs"+query, "", withCookies(cookieHeader(w.sess[user])))
	var b struct {
		Items      []api.Job `json:"items"`
		NextCursor string    `json:"next_cursor"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(w.t, json.Unmarshal(rec.Body.Bytes(), &b))
	}
	return rec.Code, b.Items, b.NextCursor
}

func jobNames(js []api.Job) []string {
	out := []string{}
	for _, j := range js {
		out = append(out, j.Project+"/"+j.Name)
	}
	return out
}

func TestScopedJobsFollowVisibilityAndScope(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		_, got, _ := w.scoped("alice", "")
		assert.Equal(t, []string{"ops/opsjob", "team/web/webjob"}, jobNames(got), "an admin sees every project's jobs")
		_, got, _ = w.scoped("alice", "?scope=team")
		assert.Equal(t, []string{"team/web/webjob"}, jobNames(got), "a scope includes the projects below it")
		_, got, _ = w.scoped("bob", "")
		assert.Equal(t, []string{"team/web/webjob"}, jobNames(got))
		_, got, _ = w.scoped("dave", "")
		assert.Equal(t, []string{"team/web/webjob"}, jobNames(got))
		code, _, _ := w.scoped("dave", "?scope=team")
		assert.Equal(t, http.StatusNotFound, code, "a project the caller has no role on")
		for _, user := range []string{"erin", "frank"} {
			_, got, _ = w.scoped(user, "")
			assert.Empty(t, got, user+": a job rule alone does not list the job here")
			c1, _, _ := w.scoped(user, "?scope=ops")
			c2, _, _ := w.scoped(user, "?scope=nope")
			assert.Equal(t, http.StatusNotFound, c1, user)
			assert.Equal(t, c1, c2, user)
		}
		// "team" must not match "teamx": a scope is a path, not a prefix of text.
		w.mustAdmin("POST", "/api/v1/projects", `{"path":"teamx"}`, 201)
		_, got, _ = w.scoped("alice", "?scope=team")
		assert.Equal(t, []string{"team/web/webjob"}, jobNames(got))
	})
}

func TestScopedJobsPageAcrossProjects(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		_, page, next := w.scoped("alice", "?limit=1")
		assert.Equal(t, []string{"ops/opsjob"}, jobNames(page))
		require.NotEmpty(t, next)
		_, page, next = w.scoped("alice", "?limit=1&cursor="+next)
		assert.Equal(t, []string{"team/web/webjob"}, jobNames(page))
		assert.Empty(t, next, "no page after the last job")
		code, _, _ := w.scoped("alice", "?cursor=!!")
		assert.Equal(t, http.StatusBadRequest, code)
	})
}

func TestGetJobReportsTheCallersRole(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		role := func(user, id string) string {
			rec := w.do(0, "GET", "/api/v1/jobs/"+id, "", withCookies(cookieHeader(w.sess[user])))
			if rec.Code != http.StatusOK {
				return http.StatusText(rec.Code)
			}
			var b struct{ Job api.Job }
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &b))
			return b.Job.MyRole
		}
		assert.Equal(t, "admin", role("alice", w.webJob))
		assert.Equal(t, "runner", role("bob", w.webJob))
		assert.Equal(t, "viewer", role("carol", w.webJob))
		assert.Equal(t, "editor", role("dave", w.webJob))
		assert.Equal(t, "runner", role("frank", w.opsJob), "a job rule alone")
		assert.Equal(t, "Not Found", role("erin", w.webJob), "no role reads as missing")
		assert.Equal(t, "Not Found", role("frank", w.webJob))
	})
}

func TestTreeRollupsCountTheVisibleSubtreeOnce(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		w := newAuthzWorld(t, st, 1)
		ctx := context.Background()
		now := time.Now().UTC()
		finish := func(jobID string, status store.RunStatus, at time.Time) {
			var run struct{ ID string }
			require.NoError(t, json.Unmarshal(w.mustAdmin("POST", "/api/v1/jobs/"+jobID+"/runs", `{"options":{}}`, 201), &run))
			cur, err := st.GetRun(ctx, run.ID)
			require.NoError(t, err)
			_, err = st.UpdateRunStatus(ctx, cur.ID, cur.Version, status, at)
			require.NoError(t, err)
		}
		finish(w.webJob, store.RunFailed, now.Add(-time.Hour))
		finish(w.webJob, store.RunFailed, now.Add(-48*time.Hour)) // too old
		finish(w.webJob, store.RunRunning, now)
		finish(w.opsJob, store.RunFailed, now.Add(-2*time.Hour))

		counts := func(user, query string) map[string][3]int {
			_, b := w.tree(user, query)
			out := map[string][3]int{}
			for _, p := range b.Projects {
				require.NotNil(t, p.Jobs, p.Path)
				out[p.Path] = [3]int{*p.Jobs, *p.Failed24h, *p.Running}
			}
			return out
		}
		assert.Equal(t, map[string][3]int{"default": {0, 0, 0}, "ops": {1, 1, 0}, "team": {1, 1, 1}}, counts("alice", ""))
		assert.Equal(t, map[string][3]int{"team/web": {1, 1, 1}}, counts("alice", "?parent=team"), "the child's counts equal the parent's own: no double counting")
		assert.Equal(t, map[string][3]int{"team": {1, 1, 1}}, counts("bob", ""))
		assert.Equal(t, map[string][3]int{"team/web": {1, 1, 1}}, counts("dave", ""), "a root below a hidden parent counts only itself")
		// ops has a failed run, but frank sees no project: nothing of it shows.
		assert.Empty(t, counts("frank", ""))
	})
}
