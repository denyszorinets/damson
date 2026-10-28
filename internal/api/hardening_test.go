package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/store"
)

const secretOptions = `{"type":"object","additionalProperties":false,"properties":{
	"token":{"type":"string","x-damson-secret":true},
	"keys":{"type":"array","x-damson-secret":true,"items":{"type":"string"}}}}`

// TestSecretMarkedOptionsAcceptOnlyRefs proves a plaintext value under an
// x-damson-secret option is rejected without being echoed, persisted or
// returned anywhere, while a secret:// reference round-trips verbatim.
func TestSecretMarkedOptionsAcceptOnlyRefs(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		job := f.createJob(t, specDoc("sec", secretOptions, "ssh", `{"host":"${{ inputs.token }}"}`))
		target := "/api/v1/jobs/" + job.Job.ID + "/runs"

		const plain = "hunter2-plaintext"
		for _, tt := range []struct{ body, loc string }{
			{`{"options":{"token":"` + plain + `"}}`, "options.token"},
			{`{"options":{"token":"secret://"}}`, "options.token"},
			{`{"options":{"keys":["secret://a","` + plain + `"]}}`, "options.keys[1]"},
			{`{"options":{"token":12345}}`, "options.token"},
		} {
			rec := f.call(t, http.MethodPost, target, tt.body)
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, tt.body)
			assert.Contains(t, rec.Body.String(), tt.loc)
			assert.NotContains(t, rec.Body.String(), plain)
			assert.NotContains(t, rec.Body.String(), "12345")
		}
		page, err := st.QueryRuns(context.Background(), store.RunFilter{}, "", 10)
		require.NoError(t, err)
		assert.Empty(t, page.Runs, "a rejected trigger persists nothing")

		const ref = "secret://vault/db#password"
		run := f.trigger(t, job.Job.ID, `{"options":{"token":"`+ref+`","keys":["secret://k1"]}}`)
		assert.JSONEq(t, `{"token":"`+ref+`","keys":["secret://k1"]}`, string(run.Options))
		stored, err := st.GetRun(context.Background(), run.ID)
		require.NoError(t, err)
		assert.JSONEq(t, string(run.Options), string(stored.Options))
		assert.JSONEq(t, `{"host":"`+ref+`"}`, string(run.Steps[0].Input))

		sse := openSSE(t, srv, "/api/v1/runs/"+run.ID+"/events", nil)
		defer sse.cancel() // before srv.Close, which waits for open streams
		assert.Contains(t, sse.mustNext().Data, ref)
		for _, p := range []string{"/api/v1/runs/" + run.ID, "/api/v1/runs"} {
			assert.NotContains(t, f.call(t, http.MethodGet, p, "").Body.String(), plain)
		}
		assert.NotContains(t, f.logs.String(), plain)
	})
}

func TestSecretMarkedOptionSchemaIsCheckedAtAuthoring(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		schema := `{"type":"object","properties":{"token":{"type":"string","x-damson-secret":true,"default":"plain-default"}}}`
		var p problemBody
		f.callOK(t, http.MethodPost, "/api/v1/jobs", wrapSpec(specDoc("s", schema, "ssh", `{"host":"h"}`)), http.StatusUnprocessableEntity, &p)
		assert.Contains(t, p.locations(), "spec.options.properties.token.default")
		assert.NotContains(t, f.logs.String(), "plain-default")
	})
}

func TestTriggerValidatesResolvedStepInput(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		opts := `{"type":"object","properties":{"n":{"type":"integer","default":5}}}`
		bad := f.createJob(t, specDoc("bad", opts, "ssh", `{"host":"${{ inputs.n }}"}`))
		var p problemBody
		f.callOK(t, http.MethodPost, "/api/v1/jobs/"+bad.Job.ID+"/runs", `{}`, http.StatusUnprocessableEntity, &p)
		assert.Contains(t, p.locations(), "steps[a].input.host")
		page, err := st.QueryRuns(context.Background(), store.RunFilter{}, "", 10)
		require.NoError(t, err)
		assert.Empty(t, page.Runs)

		// A plugin the registry does not know queues; dispatch re-validates.
		unknown := f.createJob(t, specDoc("unknown", "", "nosuch", `{"anything":1}`))
		run := f.trigger(t, unknown.Job.ID, `{}`)
		assert.Equal(t, "queued", run.Steps[0].Executions[0].State)
	})
}

func TestStreamLimit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJRStream(t, st, nil, api.StreamConfig{MaxStreams: 1})
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)

		first := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		resp, err := http.Get(srv.URL + "/api/v1/runs/" + run.ID + "/events")
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

		first.cancel()
		require.Eventually(t, func() bool {
			r, err := http.Get(srv.URL + "/api/v1/runs/" + run.ID + "/events")
			if err != nil {
				return false
			}
			defer func() { _ = r.Body.Close() }()
			return r.StatusCode == http.StatusOK
		}, 5*time.Second, 20*time.Millisecond, "the slot is released once the first stream ends")
	})
}

// TestLogHolesAreConfirmedBeforeReporting covers out-of-order chunk commits.
func TestLogHolesAreConfirmedBeforeReporting(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		t.Run("a late chunk fills the hole without a gap", func(t *testing.T) {
			f := newJRStream(t, st, nil, api.StreamConfig{PollInterval: 300 * time.Millisecond})
			srv := httptest.NewServer(f.h)
			defer srv.Close()
			run, e := f.startedRun(t)
			f.putChunk(t, e, 3, 4, "three", "four") // committed before 1-2

			s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
			f.putChunk(t, e, 1, 2, "one", "two")
			f.finish(t, run, e, store.ExecSucceeded)
			assert.Equal(t, []string{"log:1", "log:2", "log:3", "log:4", "end:"}, names(s.until("end")))
		})
		t.Run("a hole seen twice is reported while running", func(t *testing.T) {
			f := newJR(t, st, nil)
			srv := httptest.NewServer(f.h)
			defer srv.Close()
			run, e := f.startedRun(t)
			f.putChunk(t, e, 1, 2, "one", "two")
			f.putChunk(t, e, 5, 6, "five", "six")

			s := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
			assert.Equal(t, []string{"log:1", "log:2", "gap:4", "log:5", "log:6"}, names([]sseEvent{
				s.mustNext(), s.mustNext(), s.mustNext(), s.mustNext(), s.mustNext(),
			}))
			f.finish(t, run, e, store.ExecSucceeded)
			assert.Equal(t, "end", s.mustNext().Name)
		})
	})
}

// countingStore counts the expensive reads the streams make.
type countingStore struct {
	store.Store
	trees  atomic.Int64
	chunks atomic.Int64
}

func (c *countingStore) GetRunTree(ctx context.Context, id string) (store.RunTree, error) {
	c.trees.Add(1)
	return c.Store.GetRunTree(ctx, id)
}

func (c *countingStore) ListLogChunks(ctx context.Context, id string, from, to int64, limit int) ([]store.LogChunk, error) {
	c.chunks.Add(1)
	return c.Store.ListLogChunks(ctx, id, from, to, limit)
}

// TestIdleStreamsReadOnlyTheStamp: nothing changed, so no tree and no chunk
// list is loaded between polls.
func TestIdleStreamsReadOnlyTheStamp(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		cs := &countingStore{Store: st}
		f := newJR(t, cs, nil)
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		run, e := f.startedRun(t)
		f.putChunk(t, e, 1, 1, "one")

		events := openSSE(t, srv, "/api/v1/runs/"+run.ID+"/events", nil)
		events.mustNext()
		logs := openSSE(t, srv, "/api/v1/executions/"+e.ID+"/logs", nil)
		logs.mustNext()
		trees, chunks := cs.trees.Load(), cs.chunks.Load()
		time.Sleep(200 * time.Millisecond) // many poll intervals
		assert.Equal(t, trees, cs.trees.Load(), "no tree read while the run is unchanged")
		assert.Equal(t, chunks, cs.chunks.Load(), "no chunk list read while the log is unchanged")

		f.finish(t, run, e, store.ExecSucceeded)
		events.until("end")
		logs.until("end")
		assert.Greater(t, cs.trees.Load(), trees)
	})
}
