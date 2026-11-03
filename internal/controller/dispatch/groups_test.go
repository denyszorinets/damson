package dispatch_test

import (
	"testing"

	"github.com/denyszorinets/damson/internal/store"
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
