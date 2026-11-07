package api_test

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/api"
	"github.com/denyszorinets/damson/internal/pluginreg"
	"github.com/denyszorinets/damson/internal/store"
	"github.com/denyszorinets/damson/internal/store/tokens"
	"github.com/denyszorinets/damson/pkg/pluginsdk"
)

const envOptions = `{"type":"object","properties":{"env":{"type":"string","enum":["dev","prod"],"default":"dev"}}}`

type schedFixture struct {
	*secretsFixture
	cfg   api.Config
	jobID string
}

func newSchedFixture(t *testing.T, st store.Store) *schedFixture {
	t.Helper()
	reg, err := pluginreg.New([]pluginsdk.Manifest{apiManifest("ssh", "1.0.0")}, nil)
	require.NoError(t, err)
	admin, hash, err := tokens.GenerateWithPrefix(rand.Reader, tokens.AdminPrefix)
	require.NoError(t, err)
	_, err = st.CreateSetting(t.Context(), tokens.AdminSettingKey, hash)
	require.NoError(t, err)
	logs := &syncBuffer{}
	cfg := api.Config{
		Addr: "127.0.0.1:0", Store: st, Data: st, Plugins: reg, UI: ui(),
		Now:    func() time.Time { return t0 },
		Logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}
	f := &schedFixture{secretsFixture: &secretsFixture{h: api.NewServer(cfg).Handler(), logs: logs, st: st, admin: admin}, cfg: cfg}
	var job jobDetailBody
	f.callJSON(t, http.MethodPost, "/api/v1/jobs", wrapSpec(specDoc("sched-job", envOptions, "ssh", `{"host":"h"}`)), 201, &job)
	f.jobID = job.Job.ID
	return f
}

func (f *schedFixture) create(t *testing.T, body string, want int) api.Schedule {
	t.Helper()
	var s api.Schedule
	f.callJSON(t, http.MethodPost, "/api/v1/schedules", body, want, &s)
	return s
}

func TestSchedulesRequireTheAdminToken(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		pool, _, _ := tokens.New()
		body := fmt.Sprintf(`{"job_id":%q,"name":"n","cron":"* * * * *"}`, f.jobID)
		for _, tok := range []string{"", pool, tokens.AdminPrefix + "nope"} {
			for _, r := range [][3]string{
				{"GET", "/api/v1/schedules", ""}, {"GET", "/api/v1/schedules/x", ""}, {"POST", "/api/v1/schedules", body},
				{"PATCH", "/api/v1/schedules/x", `{}`}, {"DELETE", "/api/v1/schedules/x", ""},
			} {
				assert.Equal(t, http.StatusUnauthorized, f.do(r[0], r[1], r[2], tok).Code, "%s %s", r[0], r[1])
			}
		}
	})
}

func TestSchedulesCRUD(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		s := f.create(t, fmt.Sprintf(`{"job_id":%q,"name":"nightly","cron":"0 3 * * *","timezone":"Europe/Berlin","options":{"env":"prod"}}`, f.jobID), 201)
		assert.True(t, s.Enabled)
		assert.Equal(t, "run_once", s.Misfire)
		assert.EqualValues(t, 1, s.Version)
		assert.Equal(t, map[string]any{"env": "prod"}, s.Options)
		// 03:00 Berlin (CET, UTC+1) after t0 (2026-01-02 03:04 UTC) is the next day 02:00 UTC.
		require.NotNil(t, s.NextFireAt)
		assert.Equal(t, time.Date(2026, 1, 3, 2, 0, 0, 0, time.UTC), s.NextFireAt.UTC())

		var got api.Schedule
		f.callJSON(t, "GET", "/api/v1/schedules/"+s.ID, "", 200, &got)
		assert.Equal(t, s.Name, got.Name)

		// Defaults: UTC, enabled, no options.
		d := f.create(t, fmt.Sprintf(`{"job_id":%q,"name":"hourly","cron":"@hourly"}`, f.jobID), 201)
		assert.Equal(t, "UTC", d.Timezone)

		var list struct {
			Items      []api.Schedule `json:"items"`
			NextCursor string         `json:"next_cursor"`
		}
		f.callJSON(t, "GET", "/api/v1/schedules?limit=1", "", 200, &list)
		require.Len(t, list.Items, 1)
		require.NotEmpty(t, list.NextCursor)
		f.callJSON(t, "GET", "/api/v1/schedules?limit=5&cursor="+list.NextCursor, "", 200, &list)
		assert.Len(t, list.Items, 1)
		f.callJSON(t, "GET", "/api/v1/schedules?job_id=other", "", 200, &list)
		assert.Empty(t, list.Items)

		// Patch: the cron changes the next fire time; disabling clears it; re-enabling sets it.
		var p api.Schedule
		f.callJSON(t, "PATCH", "/api/v1/schedules/"+s.ID, `{"cron":"30 4 * * *","if_version":1}`, 200, &p)
		assert.EqualValues(t, 2, p.Version)
		assert.Equal(t, time.Date(2026, 1, 2, 3, 30, 0, 0, time.UTC), p.NextFireAt.UTC())
		var off, on api.Schedule
		f.callJSON(t, "PATCH", "/api/v1/schedules/"+s.ID, `{"enabled":false}`, 200, &off)
		assert.False(t, off.Enabled)
		assert.Nil(t, off.NextFireAt)
		f.callJSON(t, "PATCH", "/api/v1/schedules/"+s.ID, `{"enabled":true}`, 200, &on)
		assert.NotNil(t, on.NextFireAt)
		f.callJSON(t, "PATCH", "/api/v1/schedules/"+s.ID, `{"if_version":1,"name":"x"}`, 409, nil)
		f.callJSON(t, "PATCH", "/api/v1/schedules/nope", `{"name":"x"}`, 404, nil)

		f.create(t, fmt.Sprintf(`{"job_id":%q,"name":"hourly","cron":"@hourly"}`, f.jobID), 409)
		f.callJSON(t, "DELETE", "/api/v1/schedules/"+s.ID, "", 204, nil)
		f.callJSON(t, "GET", "/api/v1/schedules/"+s.ID, "", 404, nil)
		f.callJSON(t, "DELETE", "/api/v1/schedules/"+s.ID, "", 404, nil)

		// Writes are audited with the token's actor and without options.
		var audit struct {
			Items []api.AuditEvent `json:"items"`
		}
		f.callJSON(t, "GET", "/api/v1/audit?target_type=schedule", "", 200, &audit)
		actions := map[string]int{}
		for _, e := range audit.Items {
			actions[e.Action]++
			assert.NotContains(t, string(e.Details), "prod")
			assert.Contains(t, e.Actor, "admin-token:")
		}
		assert.Equal(t, map[string]int{"schedule.create": 2, "schedule.update": 3, "schedule.delete": 1}, actions)
	})
}

func TestScheduleValidation(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		tests := []struct {
			name, body, loc string
			want            int
		}{
			{"bad cron", `"cron":"every day"`, "body.cron", 422},
			{"never fires", `"cron":"0 0 31 2 *"`, "body.cron", 422},
			{"six fields", `"cron":"0 * * * * *"`, "body.cron", 422},
			{"bad zone", `"cron":"* * * * *","timezone":"Mars/Base"`, "body.cron", 422},
			{"options off the schema", `"cron":"* * * * *","options":{"env":"staging"}`, "options.env", 422},
			{"unknown option type", `"cron":"* * * * *","options":{"env":5}`, "options.env", 422},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				rec := f.do("POST", "/api/v1/schedules", fmt.Sprintf(`{"job_id":%q,"name":"v",%s}`, f.jobID, tt.body), f.admin)
				require.Equal(t, tt.want, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), tt.loc)
			})
		}
		assert.Equal(t, 404, f.do("POST", "/api/v1/schedules", `{"job_id":"nope","name":"v","cron":"* * * * *"}`, f.admin).Code)
		// A patch is validated too, and a refused one changes nothing.
		s := f.create(t, fmt.Sprintf(`{"job_id":%q,"name":"ok","cron":"* * * * *"}`, f.jobID), 201)
		assert.Equal(t, 422, f.do("PATCH", "/api/v1/schedules/"+s.ID, `{"cron":"nope"}`, f.admin).Code)
		var got api.Schedule
		f.callJSON(t, "GET", "/api/v1/schedules/"+s.ID, "", 200, &got)
		assert.EqualValues(t, 1, got.Version)
	})
}

// The Starter is the path schedules take to a run: the options are checked and
// defaulted, and the run is stored with the schedule trigger.
func TestStarterCreatesAScheduledRun(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		s := api.NewStarter(f.cfg)
		ctx := t.Context()

		var se *api.StartError
		_, err := s.Prepare(ctx, f.jobID, map[string]any{"env": "staging"}, api.Trigger{Type: api.TriggerSchedule})
		require.ErrorAs(t, err, &se)
		assert.Contains(t, se.Msg, "options.env")
		assert.NotContains(t, se.Msg, "staging", "a message never carries an option value")
		require.ErrorAs(t, s.CheckOptions(ctx, f.jobID, map[string]any{"env": "staging"}), &se)
		require.NoError(t, s.CheckOptions(ctx, f.jobID, nil))
		_, err = s.Prepare(ctx, "gone", nil, api.Trigger{Type: api.TriggerSchedule})
		require.ErrorAs(t, err, &se)

		p, err := s.Prepare(ctx, f.jobID, nil, api.Trigger{Type: api.TriggerSchedule, ScheduleID: "sc1", FireAt: "2026-01-02T03:00:00Z"})
		require.NoError(t, err)
		var runID string
		require.NoError(t, st.WithTx(ctx, func(tx store.Tx) error {
			var err error
			runID, err = s.Create(ctx, tx, p)
			return err
		}))
		run, err := st.GetRun(ctx, runID)
		require.NoError(t, err)
		var trig map[string]string
		require.NoError(t, json.Unmarshal(run.Trigger, &trig))
		assert.Equal(t, map[string]string{"type": "schedule", "schedule_id": "sc1", "fire_at": "2026-01-02T03:00:00Z"}, trig)
		assert.JSONEq(t, `{"env":"dev"}`, string(run.Options), "the schema default is applied")
	})
}

// A job whose options changed underneath can still have its schedules switched off.
func TestScheduleCanBeDisabledAfterTheJobOptionsChange(t *testing.T) {
	forEachBackend(t, func(t *testing.T, st store.Store) {
		f := newSchedFixture(t, st)
		s := f.create(t, fmt.Sprintf(`{"job_id":%q,"name":"n","cron":"* * * * *","options":{"env":"prod"}}`, f.jobID), 201)
		newOpts := `{"type":"object","properties":{"env":{"type":"string","enum":["blue"]}}}`
		f.callJSON(t, http.MethodPost, "/api/v1/jobs/"+f.jobID+"/versions", wrapSpec(specDoc("sched-job", newOpts, "ssh", `{"host":"h"}`)), 201, nil)
		f.callJSON(t, "PATCH", "/api/v1/schedules/"+s.ID, `{"enabled":false}`, 200, nil)
		assert.Equal(t, 422, f.do("PATCH", "/api/v1/schedules/"+s.ID, `{"enabled":true}`, f.admin).Code)
	})
}
