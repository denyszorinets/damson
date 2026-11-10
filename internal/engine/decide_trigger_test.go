package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecideAfterTriggerInsert pins the contract between the API trigger and
// run.advance: the trigger inserts the run (queued) together with the first
// step instance and its queued execution. The orchestrator then builds the
// RunState from the database, including those rows, and Decide must neither
// create the instance again nor schedule a second execution.
func TestDecideAfterTriggerInsert(t *testing.T) {
	job, err := Parse([]byte(`{"apiVersion":"damson.dev/v1","kind":"Job","metadata":{"project":"p","name":"j"},
		"spec":{"steps":[{"id":"a","plugin":"ssh"},{"id":"b","plugin":"ssh"}]}}`))
	require.NoError(t, err)
	e := newEngine(t, exprLimits())
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	step0 := StepInstanceState{
		Path: "a", StepID: "a", Status: StepPending, CreatedAt: now,
		Executions: []ExecutionView{{ID: "e1", AttemptID: "t1", State: ExecQueued}},
	}

	tests := []struct {
		name   string
		status RunStatus
		want   []string
	}{
		{name: "queued run only starts", status: RunQueued, want: []string{"start_run"}},
		{name: "running run waits", status: RunRunning, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acts, err := e.Decide(job, RunState{ID: "r1", Status: tt.status, Steps: []StepInstanceState{step0}}, now)
			require.NoError(t, err)
			var got []string
			for _, a := range acts {
				got = append(got, a.ActionType())
			}
			assert.Equal(t, tt.want, got)
			for _, a := range acts {
				assert.NotEqual(t, "create_step_instance", a.ActionType())
				assert.NotEqual(t, "schedule_execution", a.ActionType())
			}
		})
	}
}
