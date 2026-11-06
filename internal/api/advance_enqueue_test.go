package api_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

// recordingEnqueuer reads the run inside the caller's transaction, like the
// real Advancer does, and can be told to fail.
type recordingEnqueuer struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (r *recordingEnqueuer) Enqueue(ctx context.Context, tx store.Tx, runID string) error {
	if _, err := tx.GetRun(ctx, runID); err != nil {
		return err // the run must be visible inside the same transaction
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.ids = append(r.ids, runID)
	return nil
}

func newJREnqueuer(t *testing.T, st store.Store, enq api.RunEnqueuer) *jrFixture {
	t.Helper()
	reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
	require.NoError(t, err)
	srv := api.NewServer(api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), Advance: enq,
		Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler),
	})
	return &jrFixture{st: st, h: srv.Handler(), logs: &syncBuffer{}}
}

func TestTriggerAndCancelEnqueueAdvance(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		enq := &recordingEnqueuer{}
		f := newJREnqueuer(t, st, enq)
		job := f.createJob(t, specDoc("enq", "", "ssh", `{"host":"h"}`))
		run := f.trigger(t, job.Job.ID, `{}`)
		assert.Equal(t, []string{run.ID}, enq.ids)

		f.callOK(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", "", http.StatusOK, nil)
		assert.Equal(t, []string{run.ID, run.ID}, enq.ids)
		// The idempotent repeat is a stale CAS: nothing is enqueued, still 200.
		f.callOK(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", "", http.StatusOK, nil)
		assert.Len(t, enq.ids, 2)
	})
}

func TestTriggerRollsBackWhenEnqueueFails(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		enq := &recordingEnqueuer{err: errors.New("queue down")}
		f := newJREnqueuer(t, st, enq)
		job := f.createJob(t, specDoc("enq2", "", "ssh", `{"host":"h"}`))
		rec := f.call(t, http.MethodPost, "/api/v1/jobs/"+job.Job.ID+"/runs", `{}`)
		require.GreaterOrEqual(t, rec.Code, 500, rec.Body.String())
		page, err := st.ListRuns(context.Background(), job.Job.ID, "", 10)
		require.NoError(t, err)
		assert.Empty(t, page.Runs, "a run without its advance job must not exist")
	})
}

type fixedTargets []map[string]any

func (f fixedTargets) Targets(_ context.Context, _ string, pick func([]map[string]any) []map[string]any, limit int) ([]map[string]any, error) {
	out := pick(f)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// A for_each over targets that is the first step is decided at trigger time:
// it must see the targets, or it freezes an empty lane list.
func TestTriggerFreezesLanesFromTargets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
		require.NoError(t, err)
		srv := api.NewServer(api.Config{
			Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), Advance: &recordingEnqueuer{},
			Targets: fixedTargets{{"id": "id-a", "name": "a", "os": "linux"}, {"id": "id-b", "name": "b", "os": "linux"}},
			Now:     func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler),
		})
		f := &jrFixture{st: st, h: srv.Handler(), logs: &syncBuffer{}}
		job := f.createJob(t, `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"tg"},`+
			`"spec":{"steps":[{"id":"g","type":"group","for_each":"targets","steps":[{"id":"x","plugin":"ssh","with":{"host":"h"}}]}]}}`)
		run := f.trigger(t, job.Job.ID, `{}`)
		tree, err := st.GetRunTree(context.Background(), run.ID)
		require.NoError(t, err)
		var group *store.StepNode
		for i := range tree.Steps {
			if tree.Steps[i].Path == "g" {
				group = &tree.Steps[i]
			}
		}
		require.NotNil(t, group, "steps: %+v", tree.Steps)
		assert.Contains(t, string(group.Input), `"key":"a"`)
		assert.Contains(t, string(group.Input), `"key":"b"`)
		frozen, err := st.ListRunTargets(context.Background(), run.ID)
		require.NoError(t, err)
		require.Len(t, frozen, 2)
		assert.Equal(t, "id-a", frozen[0].NodeID)
		assert.Equal(t, "b", frozen[1].Name)
	})
}

// More matches than a run can freeze is a 422 at spec.targets, and no run is made.
func TestTriggerRefusesTooManyTargets(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		var many fixedTargets
		for i := 0; i <= store.MaxRunTargets; i++ {
			many = append(many, map[string]any{"id": fmt.Sprintf("id-%d", i), "name": fmt.Sprintf("n%d", i), "os": "linux"})
		}
		reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
		require.NoError(t, err)
		srv := api.NewServer(api.Config{
			Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(), Advance: &recordingEnqueuer{}, Targets: many,
			Now: func() time.Time { return t0 }, Logger: slog.New(slog.DiscardHandler),
		})
		f := &jrFixture{st: st, h: srv.Handler(), logs: &syncBuffer{}}
		job := f.createJob(t, `{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"default","name":"big"},`+
			`"spec":{"targets":{"selector":"true"},"steps":[{"id":"x","plugin":"ssh","with":{"host":"h"}}]}}`)
		rec := f.call(t, http.MethodPost, "/api/v1/jobs/"+job.Job.ID+"/runs", `{}`)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "spec.targets")
	})
}
