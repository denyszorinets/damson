package dispatch_test

import (
	"context"
	"testing"
	"time"

	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const groupSpecOK = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"one"}},
{"id":"deploy","type":"group","steps":[
  {"id":"unpack","plugin":"demo","with":{"word":"unpack ${{ steps.s1.status }}"}},
  {"id":"inner","type":"group","steps":[{"id":"build","plugin":"demo","with":{"word":"build"}}]},
  {"id":"verify","plugin":"demo","with":{"word":"verify ${{ steps.unpack.status }} ${{ steps.inner.steps.build.status }}"}}]},
{"id":"report","plugin":"demo","with":{"word":"report ${{ steps.deploy.status }}"}}]}}`

const groupSpecFail = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"one"}},
{"id":"deploy","type":"group","steps":[
  {"id":"a","plugin":"demo","with":{"word":"one"}},
  {"id":"b","plugin":"demo","with":{"word":"boom"}},
  {"id":"c","plugin":"demo","with":{"word":"two"}}]},
{"id":"report","plugin":"demo","with":{"word":"report"}}]}}`

// A job with nested groups runs end to end: steps run in order through the
// groups, the group instances start and finish, expressions see the values of
// the steps of the enclosing lists, and the run succeeds.
func TestNestedGroupsRunEndToEnd(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, groupSpecOK)
		tree := c.waitRun(c.trigger("one"), store.RunSucceeded)

		byPath := map[string]store.StepNode{}
		for _, n := range tree.Steps {
			byPath[n.Path] = n
		}
		for _, p := range []string{"s1", "deploy", "deploy/unpack", "deploy/inner", "deploy/inner/build", "deploy/verify", "report"} {
			n, ok := byPath[p]
			if !ok {
				t.Fatalf("no step %q in %v", p, paths(tree))
			}
			if n.Status != store.StepSucceeded {
				t.Fatalf("step %q = %s; want succeeded", p, n.Status)
			}
		}
		for _, p := range []string{"deploy", "deploy/inner"} {
			g := byPath[p]
			if g.Kind != store.KindGroup || len(g.Executions) != 0 || g.StartedAt == nil || g.FinishedAt == nil {
				t.Fatalf("group %q = %+v; want a finished group that ran and has no executions", p, g)
			}
		}
		waitLog(t, st, byPath["deploy/verify"].Executions[0].ID, "said verify succeeded succeeded")
		waitLog(t, st, byPath["report"].Executions[0].ID, "said report succeeded")
	})
}

// A failure inside a group skips the rest of the group, fails the group and
// then the run, and the step after the group is skipped.
func TestGroupFailureFailsGroupAndRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, groupSpecFail)
		tree := c.waitRun(c.trigger("one"), store.RunFailed)

		byPath := map[string]store.StepNode{}
		for _, n := range tree.Steps {
			byPath[n.Path] = n
		}
		want := map[string]store.StepStatus{
			"deploy": store.StepFailed, "deploy/a": store.StepSucceeded, "deploy/b": store.StepFailed,
			"deploy/c": store.StepSkipped, "report": store.StepSkipped,
		}
		for p, status := range want {
			if got := byPath[p].Status; got != status {
				t.Fatalf("step %q = %q; want %q (steps %v)", p, got, status, paths(tree))
			}
		}
		if kind, _ := store.SplitStepError(byPath["deploy"].Error); kind == "" {
			t.Fatalf("group error = %q; want the failing step's error", byPath["deploy"].Error)
		}
		if len(byPath["deploy/c"].Executions) != 0 || len(byPath["report"].Executions) != 0 {
			t.Fatal("a skipped step ran")
		}
	})
}

func paths(tree store.RunTree) []string {
	var out []string
	for _, n := range tree.Steps {
		out = append(out, n.Path+":"+string(n.Status))
	}
	return out
}

const groupSpecCancel = `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"one"}},
{"id":"deploy","type":"group","steps":[
  {"id":"hold","plugin":"demo","with":{"word":"hold"}},
  {"id":"after","plugin":"demo","with":{"word":"two"}}]},
{"id":"report","plugin":"demo","with":{"word":"report"}}]}}`

// A cancel while a step inside a group runs stops that step, cancels the rest
// of the group, the group and the run, and nothing after it runs.
func TestCancelInsideGroup(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		c := startCluster(t, st, groupSpecCancel)
		id := c.trigger("one")
		waitStepExecution(t, c, id, "deploy/hold", store.ExecRunning)

		c.requestCancel(id)
		tree := c.waitRun(id, store.RunCanceled)
		byPath := map[string]store.StepNode{}
		for _, n := range tree.Steps {
			byPath[n.Path] = n
		}
		want := map[string]store.StepStatus{
			"deploy/hold": store.StepCanceled, "deploy/after": store.StepCanceled, "deploy": store.StepCanceled, "report": store.StepCanceled,
		}
		for p, status := range want {
			if got := byPath[p].Status; got != status {
				t.Fatalf("step %q = %q; want %q (steps %v)", p, got, status, paths(tree))
			}
		}
		if len(byPath["deploy/after"].Executions) != 0 || len(byPath["report"].Executions) != 0 {
			t.Fatal("a step after the cancelled one ran")
		}
	})
}

// waitStepExecution waits until the first execution of the step at path is in state.
func waitStepExecution(t *testing.T, c *cluster, runID, path string, state store.ExecutionState) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tree, err := c.st.GetRunTree(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range tree.Steps {
			if n.Path == path && len(n.Executions) > 0 && n.Executions[0].State == state {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to be %s", path, state)
}

// A step inside a group is retried like a top-level one: the attempts keep the
// nested path, the run succeeds and the step after the group runs.
func TestRetryInsideGroup(t *testing.T) {
	spec := `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"lin"},
"spec":{"steps":[{"id":"s1","plugin":"demo","with":{"word":"one"}},
{"id":"deploy","type":"group","steps":[{"id":"build","plugin":"flaky","retry":{"max_attempts":4,"backoff":{"initial":"20ms","jitter":0}}}]},
{"id":"report","plugin":"demo","with":{"word":"report"}}]}}`
	forEachBackend(t, func(t *testing.T, st store.Store) {
		fp := &flakyPlugin{fail: 2}
		c := startClusterPlugins(t, st, spec, []pluginsdk.Plugin{fp})
		tree := c.waitRun(c.trigger("one"), store.RunSucceeded)
		var build, group store.StepNode
		for _, n := range tree.Steps {
			switch n.Path {
			case "deploy/build":
				build = n
			case "deploy":
				group = n
			}
		}
		if build.Status != store.StepSucceeded || len(build.Executions) != 3 {
			t.Fatalf("deploy/build = %s with %d attempts; want succeeded after 3", build.Status, len(build.Executions))
		}
		if group.Status != store.StepSucceeded {
			t.Fatalf("deploy = %s; want succeeded", group.Status)
		}
		if fp.callCount() != 3 {
			t.Fatalf("plugin ran %d times; want 3", fp.callCount())
		}
	})
}
