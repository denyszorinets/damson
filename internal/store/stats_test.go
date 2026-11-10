package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/denyszorinets/damson/internal/store"
)

func TestFoldLatestRuns(t *testing.T) {
	at := func(s int) *time.Time {
		v := time.Date(2026, 1, 1, 0, 0, s, 0, time.UTC)
		return &v
	}
	rows := []store.LatestRun{
		{JobID: "a", ID: "01", Status: "succeeded", CreatedAt: *at(0), StartedAt: at(0), FinishedAt: at(10)},
		{JobID: "a", ID: "03", Status: "failed", CreatedAt: *at(20), StartedAt: at(20), FinishedAt: at(25)},
		{JobID: "a", ID: "05", Status: "running", CreatedAt: *at(40), StartedAt: at(40)},
		{JobID: "b", ID: "02", Status: "pending", CreatedAt: *at(5)},
		{JobID: "c", ID: "04", Status: "canceled", CreatedAt: *at(30), FinishedAt: at(31)},
	}
	got := store.FoldLatestRuns(rows)
	require.Len(t, got, 3)

	a := got["a"]
	assert.Equal(t, store.RunRunning, a.LastStatus)
	assert.Equal(t, *at(40), a.LastRunAt)
	assert.Equal(t, at(10), a.LastSuccessAt)
	assert.Equal(t, at(25), a.LastFailureAt)
	require.NotNil(t, a.LastDuration)
	assert.Equal(t, 5*time.Second, *a.LastDuration, "the newest finished run, not the running one")

	b := got["b"]
	assert.Equal(t, store.RunPending, b.LastStatus)
	assert.Nil(t, b.LastSuccessAt)
	assert.Nil(t, b.LastDuration, "nothing finished")

	c := got["c"]
	assert.Equal(t, store.RunCanceled, c.LastStatus)
	assert.Nil(t, c.LastDuration, "a canceled run that never started has no duration")
	assert.Nil(t, c.LastFailureAt)
}
