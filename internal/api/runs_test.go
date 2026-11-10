package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/store"
)

const runOptions = `{"type":"object","required":["env"],"additionalProperties":false,"properties":{
	"env":{"type":"string","enum":["prod","dev"]},
	"count":{"type":"integer","default":3},
	"tag":{"type":"string","pattern":"^v[0-9]+$"}}}`

func TestTriggerRunOptions(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job := f.createJob(t, specDoc("opts", runOptions, "ssh", `{"host":"${{ inputs.env }}"}`))
		plain := f.createJob(t, specDoc("plain", "", "ssh", `{"host":"h"}`))
		target := "/api/v1/jobs/" + job.Job.ID + "/runs"

		tests := []struct {
			name     string
			target   string
			body     string
			wantCode int
			location string // expected problem location for 422
		}{
			{name: "missing required option", target: target, body: `{"options":{}}`, wantCode: 422, location: "options"},
			{name: "value outside enum", target: target, body: `{"options":{"env":"qa"}}`, wantCode: 422, location: "options.env"},
			{name: "wrong type", target: target, body: `{"options":{"env":"dev","count":"x"}}`, wantCode: 422, location: "options.count"},
			{name: "unknown option", target: target, body: `{"options":{"env":"dev","zzz":1}}`, wantCode: 422, location: "options"},
			{name: "pattern", target: target, body: `{"options":{"env":"dev","tag":"nope"}}`, wantCode: 422, location: "options.tag"},
			{name: "unsupported concurrency policy", target: target, body: `{"options":{"env":"dev"},"concurrency_policy":"skip"}`, wantCode: 422, location: "body.concurrency_policy"},
			{name: "unknown version", target: target, body: `{"options":{"env":"dev"},"version":9}`, wantCode: 422, location: "body.version"},
			{name: "options for a job without options", target: "/api/v1/jobs/" + plain.Job.ID + "/runs", body: `{"options":{"a":1}}`, wantCode: 422, location: "body.options"},
			{name: "unknown job", target: "/api/v1/jobs/nope/runs", body: `{}`, wantCode: 404},
			{name: "valid", target: target, body: `{"options":{"env":"dev"}}`, wantCode: 201},
			{name: "queue policy", target: target, body: `{"options":{"env":"prod"},"concurrency_policy":"queue"}`, wantCode: 201},
			{name: "no body at all", target: "/api/v1/jobs/" + plain.Job.ID + "/runs", body: ``, wantCode: 201},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := f.call(t, http.MethodPost, tt.target, tt.body)
				require.Equal(t, tt.wantCode, rec.Code, rec.Body.String())
				if tt.location != "" {
					assert.Contains(t, rec.Body.String(), tt.location)
					assert.Contains(t, rec.Header().Get("Content-Type"), "application/problem+json")
				}
			})
		}
	})
}

func TestTriggerRunCreatesStepsAtomically(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job := f.createJob(t, specDoc("opts", runOptions, "ssh", `{"host":"${{ inputs.env }}-${{ inputs.count }}"}`))
		run := f.trigger(t, job.Job.ID, `{"options":{"env":"dev"}}`)

		assert.Equal(t, "pending", run.Status)
		assert.Equal(t, 1, run.JobVersion)
		assert.JSONEq(t, `{"env":"dev","count":3}`, string(run.Options))
		assert.JSONEq(t, `{"type":"api"}`, string(run.Trigger))
		require.Len(t, run.Steps, 1)
		s := run.Steps[0]
		assert.Equal(t, "a", s.Path)
		assert.JSONEq(t, `{"host":"dev-3"}`, string(s.Input))
		require.Len(t, s.Executions, 1)
		assert.Equal(t, "queued", s.Executions[0].State)
		assert.Equal(t, 1, s.Executions[0].Attempt)
		assert.Equal(t, "default", s.Executions[0].Pool)

		var got runBody
		f.callOK(t, http.MethodGet, "/api/v1/runs/"+run.ID, "", http.StatusOK, &got)
		assert.Equal(t, run, got)
		f.callOK(t, http.MethodGet, "/api/v1/runs/nope", "", http.StatusNotFound, nil)

		var ex struct {
			ID       string `json:"id"`
			RunID    string `json:"run_id"`
			StepPath string `json:"step_path"`
			Plugin   string `json:"plugin"`
			State    string `json:"state"`
		}
		f.callOK(t, http.MethodGet, "/api/v1/executions/"+s.Executions[0].ID, "", http.StatusOK, &ex)
		assert.Equal(t, run.ID, ex.RunID)
		assert.Equal(t, "a", ex.StepPath)
		assert.Equal(t, "ssh", ex.Plugin)
		assert.NotContains(t, f.call(t, http.MethodGet, "/api/v1/executions/"+ex.ID, "").Body.String(), "attempt_id")
		f.callOK(t, http.MethodGet, "/api/v1/executions/nope", "", http.StatusNotFound, nil)
	})
}

func TestListRuns(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		a := f.createJob(t, specDoc("a", "", "ssh", `{"host":"h"}`))
		b := f.createJob(t, specDoc("b", "", "ssh", `{"host":"h"}`))
		var ids []string
		for _, j := range []string{a.Job.ID, b.Job.ID, a.Job.ID} {
			ids = append([]string{f.trigger(t, j, `{}`).ID}, ids...)
		}
		_, err := st.UpdateRunStatus(context.Background(), ids[0], 1, store.RunRunning, t0)
		require.NoError(t, err)

		type page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		collect := func(query string) []string {
			var out []string
			cursor := ""
			for range 5 {
				var p page
				target := "/api/v1/runs?limit=2" + query
				if cursor != "" {
					target += "&cursor=" + cursor
				}
				f.callOK(t, http.MethodGet, target, "", http.StatusOK, &p)
				for _, it := range p.Items {
					out = append(out, it.ID)
				}
				if p.NextCursor == "" {
					break
				}
				cursor = p.NextCursor
			}
			return out
		}
		assert.Equal(t, ids, collect(""))
		assert.Equal(t, []string{ids[0], ids[2]}, collect("&job="+a.Job.ID))
		assert.Equal(t, ids[:1], collect("&status=running"))
		assert.Empty(t, collect("&status=failed"))
		f.callOK(t, http.MethodGet, "/api/v1/runs?status=bogus", "", http.StatusUnprocessableEntity, nil)
	})
}

func TestCancelRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		job := f.createJob(t, specDoc("c", "", "ssh", `{"host":"h"}`))
		run := f.trigger(t, job.Job.ID, `{}`)

		var got struct {
			Status            string     `json:"status"`
			CancelRequestedAt *time.Time `json:"cancel_requested_at"`
		}
		f.callOK(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", "", http.StatusOK, &got)
		require.NotNil(t, got.CancelRequestedAt)
		// Idempotent while the run is still active.
		f.callOK(t, http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", "", http.StatusOK, &got)
		require.NotNil(t, got.CancelRequestedAt)
		f.callOK(t, http.MethodPost, "/api/v1/runs/nope/cancel", "", http.StatusNotFound, nil)

		// A finished run cannot be cancelled.
		done := f.trigger(t, job.Job.ID, `{}`)
		_, err := st.UpdateRunStatus(context.Background(), done.ID, 1, store.RunSucceeded, t0)
		require.NoError(t, err)
		var p problemBody
		f.callOK(t, http.MethodPost, "/api/v1/runs/"+done.ID+"/cancel", "", http.StatusConflict, &p)
		assert.Contains(t, p.Detail, "finished")
	})
}

// TestRunOptionsNeverLeakValues proves rejected option values are not echoed
// in responses or logs, and that secret references are stored and returned
// verbatim as references (hard rule 5).
func TestRunOptionsNeverLeakValues(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newJR(t, st, nil)
		schema := `{"type":"object","properties":{"token":{"type":"string"},"tag":{"type":"string","pattern":"^v[0-9]+$"}},"additionalProperties":false}`
		job := f.createJob(t, specDoc("sec", schema, "ssh", `{"host":"${{ inputs.token }}"}`))
		target := "/api/v1/jobs/" + job.Job.ID + "/runs"

		const secretValue = "hunter2-plaintext-password"
		for _, body := range []string{
			`{"options":{"tag":"` + secretValue + `"}}`,
			`{"options":{"token":["` + secretValue + `"]}}`,
			`{"options":{"` + secretValue + `":1}}`,
		} {
			rec := f.call(t, http.MethodPost, target, body)
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, body)
			assert.NotContains(t, rec.Body.String(), secretValue, body)
		}

		const ref = "secret://vault/db#password"
		run := f.trigger(t, job.Job.ID, `{"options":{"token":"`+ref+`"}}`)
		assert.Contains(t, string(run.Options), ref)
		assert.JSONEq(t, `{"host":"`+ref+`"}`, string(run.Steps[0].Input))

		assert.NotContains(t, f.logs.String(), secretValue)
		assert.False(t, strings.Contains(f.logs.String(), ref), "request logs carry paths only")
	})
}
